package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// SlackOAuthHandler handles Slack OAuth authentication endpoints.
type SlackOAuthHandler struct {
	base.HandlerBase
	svc *SlackOAuthService
	cfg *config.Config
}

// NewSlackOAuthHandler creates a new Slack OAuth handler.
func NewSlackOAuthHandler(service *SlackOAuthService, cfg *config.Config) *SlackOAuthHandler {
	return &SlackOAuthHandler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
		cfg:         cfg,
	}
}

// Login initiates the Slack OAuth flow.
// GET /api/v1/auth/slack/login?redirect_uri=...
func (h *SlackOAuthHandler) Login(writer http.ResponseWriter, req *http.Request) error {
	// Only a same-origin relative path may ride the state: the attacker mints
	// the login link, so the state nonce says nothing about this value. There
	// is no org at this point, so the default is "/".
	redirectURI := sanitizePostLoginRedirect(req.Context(), req.URL.Query().Get("redirect_uri"), "")

	// Generate and store state
	state, err := h.svc.GenerateOAuthState(req.Context(), redirectURI)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	// Build Slack OAuth URL
	slackAuthURL := h.buildSlackAuthURL(state)

	http.Redirect(writer, req, slackAuthURL, http.StatusFound)

	return nil
}

// Callback handles the OAuth callback from Slack.
// GET /api/v1/auth/slack/callback?code=...&state=...
func (h *SlackOAuthHandler) Callback(writer http.ResponseWriter, req *http.Request) error {
	code := req.URL.Query().Get("code")
	stateParam := req.URL.Query().Get("state")
	errorParam := req.URL.Query().Get("error")

	// Handle errors from Slack
	if errorParam != "" {
		return h.redirectWithError(writer, req, "/", errorParam, "OAuth was denied")
	}

	if code == "" || stateParam == "" {
		return h.redirectWithError(writer, req, "/", "INVALID_REQUEST", "Missing code or state parameter")
	}

	// Validate state and get redirect URI
	oauthState, err := h.svc.ValidateOAuthState(req.Context(), stateParam)
	if err != nil {
		return h.redirectWithError(writer, req, "/", OAuthCodeInvalidState, OAuthDescInvalidState)
	}

	// Re-check the redirect the state carries: a state minted before this
	// guard existed (rolling upgrade) must not become a redirect vector.
	returnTo := sanitizePostLoginRedirect(req.Context(), oauthState.RedirectURI, oauthState.OrgSlug)

	// Process OAuth callback
	result, err := h.svc.HandleCallback(req.Context(), code)
	if err != nil {
		return h.handleOAuthError(writer, req, returnTo, err)
	}

	// Hand the session to the dashboard through a single-use code: the
	// tokens never appear in the redirect URL (spec 2026-09-25-12).
	return finishProviderCallback(writer, req, h.svc.db, "slack", returnTo, result)
}

// Exchange trades a single-use install-callback code for the freshly
// minted session tokens. The dashboard calls this server-to-server
// immediately after landing on /d/auth/slack/complete.
//
// TODO(remove after next release): spec 2026-09-25-12
// oauth-callback-one-time-code-exchange. The Slack install callback now hands
// off through authhandoff and /d/auth/complete like every other federated
// login; this endpoint only redeems a `slack-exchange` code minted by a pod
// still running the previous release.
//
// POST /api/v1/auth/slack/exchange  body: {"code": "..."}.
func (h *SlackOAuthHandler) Exchange(writer http.ResponseWriter, req *http.Request) error {
	var body struct {
		Code string `json:"code"`
	}

	if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body.Code == "" {
		return h.WriteError(
			writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Missing or invalid code",
		)
	}

	result, err := h.svc.ExchangeSlackInstallCode(req.Context(), body.Code)
	if err != nil {
		return h.WriteError(
			writer, http.StatusBadRequest, base.ErrorCodeInvalidToken,
			"Code invalid or already used",
		)
	}

	return h.WriteJSON(writer, http.StatusOK, result)
}

// buildSlackAuthURL constructs the Slack authorization URL.
func (h *SlackOAuthHandler) buildSlackAuthURL(state string) string {
	params := url.Values{}
	params.Set("client_id", h.cfg.Slack.ClientID)
	params.Set("user_scope", "openid,email,profile")
	params.Set("redirect_uri", h.getCallbackURL())
	params.Set("state", state)

	return "https://slack.com/oauth/v2/authorize?" + params.Encode()
}

// getCallbackURL returns the OAuth callback URL for this application.
func (h *SlackOAuthHandler) getCallbackURL() string {
	return h.cfg.Server.BaseURL + "/api/v1/auth/slack/callback"
}

// redirectWithError redirects with error parameters. The destination goes
// through redirectOAuthError's same-origin guard.
func (h *SlackOAuthHandler) redirectWithError(
	writer http.ResponseWriter, req *http.Request,
	baseURI, code, description string,
) error {
	redirectOAuthError(writer, req, baseURI, code, description)

	return nil
}

// handleOAuthError handles OAuth errors by redirecting with error information.
func (h *SlackOAuthHandler) handleOAuthError(
	writer http.ResponseWriter, req *http.Request,
	redirectURI string, err error,
) error {
	slog.WarnContext(req.Context(), "Slack sign-in OAuth callback failed", "error", err)

	var code, description string

	switch {
	case errors.Is(err, ErrInvalidOAuthState):
		code = OAuthCodeInvalidState
		description = OAuthDescInvalidState
	case errors.Is(err, ErrEmailNotVerified):
		code = OAuthCodeEmailNotVerified
		description = "Email address not verified in Slack profile"
	case errors.Is(err, ErrSlackTokenExchange):
		code = OAuthCodeTokenExchange
		description = OAuthDescTokenExchange
	default:
		code = OAuthCodeFailed
		description = logOAuthFailure(req, "slack", err)
	}

	return h.redirectWithError(writer, req, redirectURI, code, description)
}
