package msteams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	mw "github.com/fclairamb/solidping/server/internal/middleware"
)

// maxActivityBody caps the inbound activity payload. Bot Framework activities
// are small; anything larger is either a bug or an attempt to make us buffer
// unbounded data on an unauthenticated (pre-verification) endpoint.
const maxActivityBody = 1 << 20 // 1 MiB

// Handler exposes the HTTP surface of the Teams bot integration.
type Handler struct {
	base.HandlerBase
	svc      *Service
	cfg      *config.Config
	verifier *Verifier
}

// NewHandler creates a Microsoft Teams bot handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
		cfg:         cfg,
		verifier:    NewVerifier(cfg.MSTeams.AppID),
	}
}

// SetVerifier replaces the token verifier. Tests use it to point at a fake
// Bot Framework metadata/JWKS server.
func (h *Handler) SetVerifier(v *Verifier) { h.verifier = v }

// StatusResponse is returned by GetStatus — the Teams counterpart of Slack's
// socket-status endpoint. It is what the dashboard setup page reads to
// explain *why* the bot is unavailable rather than just failing silently.
//
// This endpoint is org-scoped and authenticated on purpose. An earlier
// revision served it publicly, which handed any anonymous caller the pinned
// tenant GUID and a live count of installed Microsoft 365 tenants — a free
// customer-enumeration oracle. There is no public counterpart: the messaging
// endpoint is the only route on this integration that anonymous callers need,
// and it already answers 503 when the feature is off.
type StatusResponse struct {
	Enabled bool `json:"enabled"`
	// Configured is true when an app ID and secret are present.
	Configured bool `json:"configured"`
	// AppID lets the setup page show which Entra app this instance runs as.
	AppID string `json:"appId,omitempty"`
	// MessagingEndpoint is the URL that must be reachable from Microsoft over
	// public HTTPS — the single most common self-hosted misconfiguration.
	MessagingEndpoint string `json:"messagingEndpoint"`
	// InstalledTenants counts distinct Entra tenants with a live connection.
	InstalledTenants int `json:"installedTenants"`
	// SingleTenant echoes the configured tenant allow-list, "" when
	// multi-tenant.
	SingleTenant string `json:"singleTenant,omitempty"`
}

// MessagingEndpointPath is the path Microsoft must POST activities to.
const MessagingEndpointPath = "/api/v1/integrations/msteams/messages"

// GetStatus reports whether the Teams bot is usable on this instance.
//
// Route: GET /api/v1/orgs/:org/integrations/msteams/status (authenticated).
func (h *Handler) GetStatus(writer http.ResponseWriter, req *http.Request) error {
	resp := StatusResponse{
		Enabled:           h.svc.Enabled(),
		Configured:        h.svc.Configured(),
		AppID:             h.cfg.MSTeams.AppID,
		MessagingEndpoint: h.cfg.Server.BaseURL + MessagingEndpointPath,
		SingleTenant:      h.cfg.MSTeams.TenantID,
	}

	if count, err := h.svc.CountInstalledTenants(req.Context()); err == nil {
		resp.InstalledTenants = count
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// linkRequest is the optional body of StartLink.
type linkRequest struct {
	ConnectionUID string `json:"connectionUid"`
}

// StartLink mints a one-time linking code for the authenticated caller's
// organization, creating an unlinked connection when none is specified.
//
// The org comes from the verified route context (RequireOrgAccess), never
// from client input — that binding is the whole point: the code proves that
// whoever later quotes it inside a Microsoft 365 tenant is acting for THIS
// organization.
//
// Route: POST /api/v1/orgs/:org/integrations/msteams/link-code.
func (h *Handler) StartLink(writer http.ResponseWriter, req *http.Request) error {
	org, ok := mw.GetOrganizationFromContext(req.Context())
	if !ok || org == nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound,
			"Organization not found")
	}

	var body linkRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: "body", Message: "Invalid JSON format"},
		})
	}

	pending, err := h.svc.StartLink(req.Context(), org.UID, body.ConnectionUID)
	if err != nil {
		switch {
		case errors.Is(err, ErrConnectionNotFound):
			return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeIntegrationNotFound,
				"Channel not found")
		case errors.Is(err, ErrNotMSTeamsBotChannel):
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
				"Channel is not of type msteams-bot")
		case errors.Is(err, ErrConnectionAlreadyLinked):
			return h.WriteError(writer, http.StatusConflict, base.ErrorCodeConflict,
				"This integration is already linked to a Microsoft 365 tenant")
		default:
			return h.WriteInternalError(writer, req, err)
		}
	}

	return h.WriteJSON(writer, http.StatusOK, pending)
}

// GetDestinations returns the conversation references captured for a
// connection.
//
// Route: GET /api/v1/orgs/:org/channels/:uid/msteams/destinations.
func (h *Handler) GetDestinations(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	channelUID := httpx.Param(req, "uid")

	resp, err := h.svc.GetDestinations(req.Context(), orgSlug, channelUID)
	if err != nil {
		switch {
		case errors.Is(err, ErrConnectionNotFound), errors.Is(err, ErrOrganizationNotFound):
			return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeIntegrationNotFound,
				"Channel not found")
		case errors.Is(err, ErrNotMSTeamsBotChannel):
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
				"Channel is not of type msteams-bot")
		default:
			return h.WriteInternalError(writer, req, err)
		}
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// DownloadManifest serves the generated Teams app package (a zip holding
// manifest.json plus the two icons), pre-filled with this instance's app ID
// and public URL so setup requires no hand-editing.
//
// Org-scoped and authenticated: the package is a setup artifact for admins,
// and serving it anonymously would advertise the instance's Entra app id to
// anyone who asks.
//
// Route: GET /api/v1/orgs/:org/integrations/msteams/manifest.zip.
func (h *Handler) DownloadManifest(writer http.ResponseWriter, request *http.Request) error {
	if h.cfg.MSTeams.AppID == "" {
		return h.WriteError(writer, http.StatusConflict, base.ErrorCodeValidationError,
			"Set the Microsoft Teams app ID in the server settings before downloading the app package")
	}

	pkg, err := BuildAppPackage(h.cfg.MSTeams.AppID, h.cfg.Server.BaseURL)
	if err != nil {
		return h.WriteInternalError(writer, request, err)
	}

	writer.Header().Set("Content-Type", "application/zip")
	writer.Header().Set("Content-Disposition", `attachment; filename="solidping-teams-app.zip"`)
	writer.WriteHeader(http.StatusOK)

	if _, err := writer.Write(pkg); err != nil {
		return fmt.Errorf("writing teams app package: %w", err)
	}

	return nil
}

// HandleMessages is the Bot Framework messaging endpoint.
//
// Route: POST /api/v1/integrations/msteams/messages.
//
// Every inbound activity is authenticated by JWT (see verify.go) before it is
// dispatched: unlike Slack this endpoint is genuinely public with no shared
// secret, so an unverified activity is never acted upon. Verification
// failures answer 401; successful verification always answers 200, even when
// dispatch fails, because Bot Framework retries non-2xx responses and a
// retried command would run twice.
func (h *Handler) HandleMessages(writer http.ResponseWriter, req *http.Request) error {
	if !h.svc.Enabled() {
		return h.WriteError(writer, http.StatusServiceUnavailable, base.ErrorCodeValidationError,
			"The Microsoft Teams bot is not enabled on this instance")
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, maxActivityBody))
	if err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Failed to read request body")
	}

	var activity Activity
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&activity); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Invalid activity payload")
	}

	if err := h.verifyActivity(req, &activity); err != nil {
		slog.WarnContext(req.Context(), "Rejected Microsoft Teams activity",
			"error", err, "activity_type", activity.Type)

		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized,
			"Invalid Bot Framework token")
	}

	if err := DispatchActivity(req.Context(), h.svc, &activity); err != nil {
		slog.ErrorContext(req.Context(), "Failed to handle Microsoft Teams activity",
			"activity_type", activity.Type, "error", err)
		// Fall through to 200: Bot Framework retries non-2xx deliveries.
	}

	writer.WriteHeader(http.StatusOK)

	return nil
}

// verifyActivity validates the inbound Bot Connector token against the
// activity it was delivered with.
func (h *Handler) verifyActivity(req *http.Request, activity *Activity) error {
	token, err := TokenFromHeader(req.Header.Get("Authorization"))
	if err != nil {
		return err
	}

	if _, err := h.verifier.VerifyToken(req.Context(), token, activity.ServiceURL); err != nil {
		return err
	}

	return nil
}

// clientFor builds a Bot Connector client for an activity's service URL.
func (h *Handler) clientFor(activity *Activity) *Client {
	serviceURL := normalizeServiceURL(activity.ServiceURL)

	if h.svc.newBotClient != nil {
		return h.svc.newBotClient(serviceURL)
	}

	return NewClient(h.svc.cfg.MSTeams.AppID, h.svc.cfg.MSTeams.AppSecret, serviceURL)
}

// reply posts a message activity back into the conversation the inbound
// activity came from, threading it under the original message when there is
// one (Teams renders that as a reply in the channel's conversation).
func (h *Handler) reply(ctx context.Context, inbound *Activity, outbound *Activity) error {
	conversationID := inbound.ConversationID()
	if conversationID == "" {
		return fmt.Errorf("%w: activity has no conversation", ErrBotConnector)
	}

	client := h.clientFor(inbound)

	if inbound.ID != "" {
		if _, err := client.ReplyToActivity(ctx, conversationID, inbound.ID, outbound); err != nil {
			return fmt.Errorf("replying to Teams activity: %w", err)
		}

		return nil
	}

	if _, err := client.SendToConversation(ctx, conversationID, outbound); err != nil {
		return fmt.Errorf("posting Teams activity: %w", err)
	}

	return nil
}

// replyCard replies with an Adaptive Card plus a plain-text fallback.
func (h *Handler) replyCard(ctx context.Context, inbound *Activity, fallback string, card *AdaptiveCard) error {
	return h.reply(ctx, inbound, NewCardMessage(fallback, card))
}

// replyText replies with a plain-text message.
func (h *Handler) replyText(ctx context.Context, inbound *Activity, text string) error {
	return h.reply(ctx, inbound, NewTextMessage(text))
}

// replyError replies with a warning-styled card.
func (h *Handler) replyError(ctx context.Context, inbound *Activity, message string) error {
	return h.replyCard(ctx, inbound, message, SimpleCard("⚠️ "+message, "", CardColorWarning))
}
