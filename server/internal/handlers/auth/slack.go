package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/uptrace/bunrouter"

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
func (h *SlackOAuthHandler) Login(writer http.ResponseWriter, req bunrouter.Request) error {
	redirectURI := req.URL.Query().Get("redirect_uri")
	if redirectURI == "" {
		redirectURI = "/" // Default to root
	}

	// Generate and store state
	state, err := h.svc.GenerateOAuthState(req.Context(), redirectURI)
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	// Build Slack OAuth URL
	slackAuthURL := h.buildSlackAuthURL(state)

	http.Redirect(writer, req.Request, slackAuthURL, http.StatusFound)

	return nil
}

// Callback handles the OAuth callback from Slack.
// GET /api/v1/auth/slack/callback?code=...&state=...
func (h *SlackOAuthHandler) Callback(writer http.ResponseWriter, req bunrouter.Request) error {
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

	// Process OAuth callback
	result, err := h.svc.HandleCallback(req.Context(), code)
	if err != nil {
		return h.handleOAuthError(writer, req, oauthState.RedirectURI, err)
	}

	// Redirect with tokens
	redirectURL := h.buildSuccessRedirect(oauthState.RedirectURI, result)
	http.Redirect(writer, req.Request, redirectURL, http.StatusFound)

	return nil
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

// buildSuccessRedirect constructs the redirect URL with tokens.
func (h *SlackOAuthHandler) buildSuccessRedirect(baseURI string, result *SlackOAuthResult) string {
	parsedURL, err := url.Parse(baseURI)
	if err != nil {
		// Fallback to root if parsing fails
		parsedURL, _ = url.Parse("/")
	}

	query := parsedURL.Query()
	query.Set("access_token", result.AccessToken)
	query.Set("refresh_token", result.RefreshToken)
	query.Set("org", result.OrgSlug)
	parsedURL.RawQuery = query.Encode()

	return parsedURL.String()
}

// redirectWithError redirects with error parameters.
func (h *SlackOAuthHandler) redirectWithError(
	writer http.ResponseWriter, req bunrouter.Request,
	baseURI, code, description string,
) error {
	parsedURL, err := url.Parse(baseURI)
	if err != nil {
		// Fallback to root if parsing fails
		parsedURL, _ = url.Parse("/")
	}

	query := parsedURL.Query()
	query.Set("error", code)
	query.Set("error_description", description)
	parsedURL.RawQuery = query.Encode()

	http.Redirect(writer, req.Request, parsedURL.String(), http.StatusFound)

	return nil
}

// handleOAuthError handles OAuth errors by redirecting with error information.
func (h *SlackOAuthHandler) handleOAuthError(
	writer http.ResponseWriter, req bunrouter.Request,
	redirectURI string, err error,
) error {
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
		description = fmt.Sprintf("OAuth failed: %v", err)
	}

	return h.redirectWithError(writer, req, redirectURI, code, description)
}
