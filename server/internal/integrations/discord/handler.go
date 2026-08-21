package discord

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	mw "github.com/fclairamb/solidping/server/internal/middleware"
)

// Handler provides the HTTP handlers for the Discord integration.
type Handler struct {
	base.HandlerBase
	svc *Service
	cfg *config.Config
}

// NewHandler creates a new Discord integration handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
		cfg:         cfg,
	}
}

// installURLRequest is the optional JSON body for BuildInstallURLForOrg.
type installURLRequest struct {
	ChannelUID string `json:"channelUid"`
}

// installURLResponse is returned by BuildInstallURLForOrg.
type installURLResponse struct {
	URL string `json:"url"`
}

// BuildInstallURLForOrg mints a Discord bot install URL scoped to the
// authenticated caller's organization.
//
// There is deliberately NO unauthenticated install entry point (the Slack
// package has one for its Marketplace listing). An anonymous Discord install
// would have to trust a caller-supplied org, which is precisely the hole spec
// 2026-07-05-01 closed on the Slack side.
//
// POST /api/v1/orgs/:org/integrations/discord/install-url
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
		switch {
		case errors.Is(err, ErrConnectionNotFound):
			return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeIntegrationNotFound,
				"Channel not found")
		case errors.Is(err, ErrBotNotConfigured):
			return h.WriteError(writer, http.StatusConflict, base.ErrorCodeChannelNotConnected,
				"Discord bot is not configured on this instance")
		default:
			return h.WriteInternalError(writer, req, err)
		}
	}

	return h.WriteJSON(writer, http.StatusOK, installURLResponse{URL: authorizeURL})
}

// installErrorPage is where a failed install lands. Mirrors the Slack flow.
const installErrorPage = "https://www.solidping.io/saas/install-error"

// OAuthCallback completes a bot install and bounces the operator back into the
// dashboard on the integration that was just created or updated.
//
// GET /api/v1/integrations/discord/oauth.
func (h *Handler) OAuthCallback(writer http.ResponseWriter, req *http.Request) error {
	code := req.URL.Query().Get("code")
	state := req.URL.Query().Get("state")

	if errorParam := req.URL.Query().Get("error"); errorParam != "" {
		slog.WarnContext(req.Context(), "OAuth error from Discord", "error", errorParam)
		h.redirectInstallError(writer, req, "oauth_failed")

		return nil
	}

	if code == "" || state == "" {
		h.redirectInstallError(writer, req, "state_invalid")

		return nil
	}

	result, err := h.svc.HandleOAuthCallback(req.Context(), code, state)
	if err != nil {
		h.redirectInstallError(writer, req, installErrorReason(req, err))

		return nil
	}

	target := h.cfg.Server.BaseURL + "/dash0/orgs/" + url.PathEscape(result.OrgSlug) +
		"/integrations/" + url.PathEscape(result.ConnectionUID)
	http.Redirect(writer, req, target, http.StatusFound)

	return nil
}

// installErrorReason maps a callback failure to the machine-readable reason
// code the friendly install-error page renders.
func installErrorReason(req *http.Request, err error) string {
	switch {
	case errors.Is(err, ErrInvalidState):
		slog.WarnContext(req.Context(), "Discord install state rejected", "error", err)

		return "state_invalid"
	case errors.Is(err, ErrGuildMissing):
		slog.WarnContext(req.Context(), "Discord install returned no guild", "error", err)

		return "guild_missing"
	case errors.Is(err, ErrOAuthFailed):
		slog.ErrorContext(req.Context(), "Discord OAuth exchange failed", "error", err)

		return "oauth_failed"
	default:
		slog.ErrorContext(req.Context(), "Discord install failed", "error", err)

		return "unknown"
	}
}

// redirectInstallError sends the operator to the friendly install-error page.
func (h *Handler) redirectInstallError(writer http.ResponseWriter, req *http.Request, reason string) {
	http.Redirect(writer, req, installErrorPage+"?reason="+url.QueryEscape(reason), http.StatusFound)
}

// GetDestinations returns the Discord text channels available for an
// integration.
//
// Route: GET /api/v1/orgs/:org/channels/:uid/discord/destinations.
func (h *Handler) GetDestinations(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	channelUID := httpx.Param(req, "uid")

	resp, err := h.svc.GetDestinations(req.Context(), orgSlug, channelUID)
	if err != nil {
		switch {
		case errors.Is(err, ErrConnectionNotFound), errors.Is(err, ErrOrganizationNotFound):
			return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeIntegrationNotFound,
				"Channel not found")
		case errors.Is(err, ErrNotDiscordChannel):
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
				"Channel is not of type discord")
		case errors.Is(err, ErrDiscordNotConnected), errors.Is(err, ErrBotNotConfigured):
			return h.WriteError(writer, http.StatusConflict, base.ErrorCodeChannelNotConnected,
				"Discord server is not connected — install the SolidPing bot")
		default:
			// Discord API failures surface as 502 so a bad bot token is
			// distinguishable from a general server error.
			return h.WriteError(writer, http.StatusBadGateway, base.ErrorCodeInternalError,
				"Could not connect to the Discord server")
		}
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}
