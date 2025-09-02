package slack

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Handler provides HTTP handlers for Slack integration endpoints.
type Handler struct {
	base.HandlerBase
	svc *Service
	cfg *config.Config
}

// NewHandler creates a new Slack handler.
func NewHandler(service *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
		cfg:         cfg,
	}
}

// OAuthCallback handles the OAuth callback from Slack.
func (h *Handler) OAuthCallback(writer http.ResponseWriter, req bunrouter.Request) error {
	code := req.URL.Query().Get("code")
	state := req.URL.Query().Get("state")
	errorParam := req.URL.Query().Get("error")

	// Handle OAuth errors from Slack
	if errorParam != "" {
		slog.WarnContext(req.Context(), "OAuth error from Slack", "error", errorParam)
		// Redirect to frontend with error
		redirectURL := "/integrations?error=" + url.QueryEscape(errorParam)
		http.Redirect(writer, req.Request, redirectURL, http.StatusFound)

		return nil
	}

	if code == "" {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Missing code parameter")
	}

	if state == "" {
		slog.DebugContext(req.Context(), "No state parameter passed")
	}

	result, err := h.svc.HandleOAuthCallback(req.Context(), code, state)
	if err != nil {
		slog.ErrorContext(req.Context(), "OAuth callback failed", "error", err)

		switch {
		case errors.Is(err, ErrEmailRequired):
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
				"Email is required in your Slack profile")
		case errors.Is(err, ErrOAuthFailed):
			return h.WriteError(writer, http.StatusBadGateway, base.ErrorCodeInternalError,
				"OAuth exchange failed")
		default:
			return h.WriteInternalError(writer, err)
		}
	}

	// Redirect to frontend success page with tokens
	redirectURL := fmt.Sprintf("/dashboard/org/%s/integrations/slack/%s?success=true&access_token=%s&refresh_token=%s",
		result.OrgSlug,
		result.ConnectionUID,
		url.QueryEscape(result.AccessToken),
		url.QueryEscape(result.RefreshToken),
	)
	http.Redirect(writer, req.Request, redirectURL, http.StatusFound)

	return nil
}

// HandleEvents handles incoming Slack events.
func (h *Handler) HandleEvents(writer http.ResponseWriter, req bunrouter.Request) error {
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
func (h *Handler) HandleCommand(writer http.ResponseWriter, req bunrouter.Request) error {
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
			ResponseType: "ephemeral",
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
func (h *Handler) HandleInteraction(writer http.ResponseWriter, req bunrouter.Request) error {
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
