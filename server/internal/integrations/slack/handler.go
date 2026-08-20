package slack

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	mw "github.com/fclairamb/solidping/server/internal/middleware"
)

// GetDestinations returns the list of Slack channels and DM targets available
// for the given integration connection (identified by :uid within :org).
//
// Route: GET /api/v1/orgs/:org/channels/:uid/slack/destinations.
func (h *Handler) GetDestinations(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	channelUID := httpx.Param(req, "uid")

	resp, err := h.svc.GetDestinations(req.Context(), orgSlug, channelUID)
	if err != nil {
		switch {
		case errors.Is(err, ErrConnectionNotFound), errors.Is(err, ErrOrganizationNotFound):
			return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeIntegrationNotFound,
				"Channel not found")
		case errors.Is(err, ErrNotSlackChannel):
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
				"Channel is not of type slack")
		case errors.Is(err, ErrSlackNotConnected):
			return h.WriteError(writer, http.StatusConflict, base.ErrorCodeChannelNotConnected,
				"Slack channel is not connected — install the Slack app")
		default:
			// Wrap Slack API failures as 502 so callers can distinguish bot-token
			// issues from general server errors.
			return h.WriteError(writer, http.StatusBadGateway, base.ErrorCodeInternalError,
				"Could not connect to Slack workspace")
		}
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// Handler provides HTTP handlers for Slack integration endpoints.
type Handler struct {
	base.HandlerBase
	svc        *Service
	cfg        *config.Config
	supervisor *SlackSocketSupervisor
}

// NewHandler creates a new Slack handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
		cfg:         cfg,
	}
}

// SetSocketSupervisor attaches a running Socket Mode supervisor so the
// GetSocketStatus endpoint can surface its state. Optional — call only on
// nodes that actually run the supervisor.
func (h *Handler) SetSocketSupervisor(s *SlackSocketSupervisor) {
	h.supervisor = s
}

// GetSocketStatus returns the current Slack Socket Mode supervisor status.
//
// Route: GET /api/v1/integrations/slack/socket/status.
func (h *Handler) GetSocketStatus(writer http.ResponseWriter, _ *http.Request) error {
	if h.supervisor == nil {
		return h.WriteJSON(writer, http.StatusOK, SocketStatus{Enabled: false})
	}

	return h.WriteJSON(writer, http.StatusOK, h.supervisor.GetStatus())
}

// installErrorPage is where we send the user when an install fails. The
// page lives in the marketing site and renders a friendly explanation
// based on the `reason` query parameter.
const installErrorPage = "https://www.solidping.io/saas/install-error"

// Install is the public Marketplace direct-install entry point. It mints a
// fresh CSRF state and 302s to Slack. No auth required — Slack hits this
// URL with no session.
//
// It intentionally does NOT honor `org` or `channelUid` query params — this
// endpoint is unauthenticated, so trusting caller-supplied org/channel
// targeting here would let anyone auto-join themselves into an arbitrary org
// or overwrite an arbitrary channel's Slack credentials by completing OAuth
// with their own workspace (see spec 2026-07-05-01, "Security problem").
// Org-scoped installs must go through the authenticated
// POST /orgs/:org/integrations/slack/install-url endpoint instead
// (BuildInstallURLForOrg below), which takes the org from the session.
//
// GET /api/v1/integrations/slack/install[?source=marketplace].
func (h *Handler) Install(writer http.ResponseWriter, req *http.Request) error {
	source := req.URL.Query().Get("source")

	authorizeURL, err := h.svc.BuildInstallURL(req.Context(), source, "", "")
	if err != nil {
		slog.ErrorContext(req.Context(), "Failed to build Slack install URL", "error", err)
		h.redirectInstallError(writer, req, "unknown")

		return nil
	}

	http.Redirect(writer, req, authorizeURL, http.StatusFound)

	return nil
}

// installURLRequest is the optional JSON body for BuildInstallURLForOrg.
type installURLRequest struct {
	ChannelUID string `json:"channelUid"`
}

// installURLResponse is returned by BuildInstallURLForOrg.
type installURLResponse struct {
	URL string `json:"url"`
}

// BuildInstallURLForOrg mints a Slack OAuth authorization URL scoped to the
// authenticated caller's organization. Unlike Install, this endpoint sits
// behind the normal org-auth chain (RequireAuth + RequireOrgAccess), so the
// org comes from the verified route context — never from client input — and
// RequireOrgAccess already rejects non-members before this handler runs.
//
// POST /api/v1/orgs/:org/integrations/slack/install-url
// Body (optional): { "channelUid": "<uid>" }.
func (h *Handler) BuildInstallURLForOrg(writer http.ResponseWriter, req *http.Request) error {
	org, ok := mw.GetOrganizationFromContext(req.Context())
	if !ok || org == nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound,
			"Organization not found")
	}

	var body installURLRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	authorizeURL, err := h.svc.BuildOrgInstallURL(req.Context(), org.UID, org.Slug, body.ChannelUID)
	if err != nil {
		if errors.Is(err, ErrConnectionNotFound) {
			return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeIntegrationNotFound,
				"Channel not found")
		}

		slog.ErrorContext(req.Context(), "Failed to build org-scoped Slack install URL", "error", err)

		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, installURLResponse{URL: authorizeURL})
}

// OAuthCallback handles the OAuth callback from Slack.
func (h *Handler) OAuthCallback(writer http.ResponseWriter, req *http.Request) error {
	code := req.URL.Query().Get("code")
	state := req.URL.Query().Get("state")
	errorParam := req.URL.Query().Get("error")

	if errorParam != "" {
		slog.WarnContext(req.Context(), "OAuth error from Slack", "error", errorParam)
		h.redirectInstallError(writer, req, "oauth_failed")

		return nil
	}

	if code == "" || state == "" {
		h.redirectInstallError(writer, req, "state_invalid")

		return nil
	}

	result, err := h.svc.HandleOAuthCallback(req.Context(), code, state)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidState):
			slog.WarnContext(req.Context(), "Slack OAuth state rejected", "error", err)
			h.redirectInstallError(writer, req, "state_invalid")
		case errors.Is(err, ErrEmailRequired):
			slog.WarnContext(req.Context(), "Slack OAuth email missing", "error", err)
			h.redirectInstallError(writer, req, "email_missing")
		case errors.Is(err, ErrOAuthFailed):
			slog.ErrorContext(req.Context(), "Slack OAuth exchange failed", "error", err)
			h.redirectInstallError(writer, req, "oauth_failed")
		default:
			slog.ErrorContext(req.Context(), "Slack OAuth callback failed", "error", err)
			h.redirectInstallError(writer, req, "unknown")
		}

		return nil
	}

	// The install succeeded but the org did not admit the installing user:
	// there is no org-scoped session to hand over (and no refresh token for
	// the exchange payload), so send them to the same request-access surface
	// every other federated login uses for a pending membership.
	if result.Pending {
		slog.InfoContext(req.Context(), "Slack install completed without org membership",
			"org_slug", result.OrgSlug, "user_uid", result.UserUID)
		auth.RedirectPendingMembership(writer, req,
			h.cfg.Server.BaseURL, result.OrgSlug, result.AccessToken, result.ExpiresIn)

		return nil
	}

	exchangeCode, err := h.svc.IssueExchangeCode(req.Context(), result)
	if err != nil {
		slog.ErrorContext(req.Context(), "Failed to issue Slack exchange code", "error", err)
		h.redirectInstallError(writer, req, "unknown")

		return nil
	}

	completeURL := h.cfg.Server.BaseURL + "/dash0/auth/slack/complete?code=" + url.QueryEscape(exchangeCode)
	http.Redirect(writer, req, completeURL, http.StatusFound)

	return nil
}

// redirectInstallError sends the user to the marketing site's friendly
// install-error page with a machine-readable reason code so the page can
// surface the right message and Try-Again link.
func (h *Handler) redirectInstallError(
	writer http.ResponseWriter, req *http.Request, reason string,
) {
	target := installErrorPage + "?reason=" + url.QueryEscape(reason)
	http.Redirect(writer, req, target, http.StatusFound)
}

// HandleEvents handles incoming Slack events.
func (h *Handler) HandleEvents(writer http.ResponseWriter, req *http.Request) error {
	var event Event
	if err := json.NewDecoder(req.Body).Decode(&event); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Invalid JSON payload")
	}

	// Handle URL verification challenge
	if event.Type == "url_verification" {
		return h.WriteJSON(writer, http.StatusOK, map[string]string{
			"challenge": event.Challenge,
		})
	}

	// Handle actual events
	if event.Type == "event_callback" {
		if err := h.handleEvent(req.Context(), &event); err != nil {
			slog.ErrorContext(req.Context(), "Failed to handle event",
				"event_type", event.Event.Type,
				"error", err,
			)
			// Return 200 to Slack to prevent retries
		}
	}

	// Always return 200 to acknowledge receipt
	writer.WriteHeader(http.StatusOK)

	return nil
}

// HandleCommand handles incoming slash commands.
func (h *Handler) HandleCommand(writer http.ResponseWriter, req *http.Request) error {
	if err := req.ParseForm(); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Invalid form data")
	}

	cmd := Command{
		Token:       req.FormValue("token"),
		TeamID:      req.FormValue("team_id"),
		TeamDomain:  req.FormValue("team_domain"),
		ChannelID:   req.FormValue("channel_id"),
		ChannelName: req.FormValue("channel_name"),
		UserID:      req.FormValue("user_id"),
		UserName:    req.FormValue("user_name"),
		Command:     req.FormValue("command"),
		Text:        req.FormValue("text"),
		ResponseURL: req.FormValue("response_url"),
		TriggerID:   req.FormValue("trigger_id"),
		APIAppID:    req.FormValue("api_app_id"),
		ThreadTS:    req.FormValue("thread_ts"),
	}

	response, err := h.handleCommand(req.Context(), &cmd)
	if err != nil {
		slog.ErrorContext(req.Context(), "Failed to handle command",
			"command", cmd.Command,
			"error", err,
		)

		// Return error message to user
		return h.WriteJSON(writer, http.StatusOK, &MessageResponse{
			ResponseType: ResponseTypeEphemeral,
			Text:         "Sorry, an error occurred while processing your command.",
		})
	}

	// If no response (posted via API), just acknowledge with empty 200
	if response == nil {
		writer.WriteHeader(http.StatusOK)

		return nil
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}

// HandleInteraction handles incoming interactions (buttons, modals, shortcuts).
func (h *Handler) HandleInteraction(writer http.ResponseWriter, req *http.Request) error {
	if err := req.ParseForm(); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Invalid form data")
	}

	payloadStr := req.FormValue("payload")
	if payloadStr == "" {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Missing payload")
	}

	var interaction Interaction
	if err := json.Unmarshal([]byte(payloadStr), &interaction); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Invalid payload JSON")
	}

	response, err := h.handleInteraction(req.Context(), &interaction)
	if err != nil {
		slog.ErrorContext(req.Context(), "Failed to handle interaction",
			"type", interaction.Type,
			"error", err,
		)

		// Return empty response for most errors
		writer.WriteHeader(http.StatusOK)

		return nil
	}

	if response != nil {
		return h.WriteJSON(writer, http.StatusOK, response)
	}

	writer.WriteHeader(http.StatusOK)

	return nil
}
