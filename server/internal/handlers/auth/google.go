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

// GoogleOAuthHandler handles Google OAuth authentication endpoints.
type GoogleOAuthHandler struct {
	base.HandlerBase
	svc *GoogleOAuthService
	cfg *config.Config
}

// NewGoogleOAuthHandler creates a new Google OAuth handler.
func NewGoogleOAuthHandler(service *GoogleOAuthService, cfg *config.Config) *GoogleOAuthHandler {
	return &GoogleOAuthHandler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
		cfg:         cfg,
	}
}

// Login initiates the Google OAuth flow.
// GET /api/v1/auth/google/login?org=...&redirect_uri=...
func (h *GoogleOAuthHandler) Login(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.URL.Query().Get("org")
	if orgSlug == "" {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "org parameter is required")
	}

	// Validate that the org exists
	if _, err := h.svc.db.GetOrganizationBySlug(req.Context(), orgSlug); err != nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	}

	redirectURI := req.URL.Query().Get("redirect_uri")
	if redirectURI == "" {
		redirectURI = "/dash0/orgs/" + orgSlug
	}

	// Generate and store state with org slug
	state, err := h.svc.GenerateOAuthState(req.Context(), redirectURI, orgSlug)
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	// Build Google OAuth URL
	googleAuthURL := h.buildGoogleAuthURL(state)

	http.Redirect(writer, req.Request, googleAuthURL, http.StatusFound)

	return nil
}

// Callback handles the OAuth callback from Google.
// GET /api/v1/auth/google/callback?code=...&state=...
func (h *GoogleOAuthHandler) Callback(writer http.ResponseWriter, req bunrouter.Request) error {
	code := req.URL.Query().Get("code")
	stateParam := req.URL.Query().Get("state")
	errorParam := req.URL.Query().Get("error")

	// Handle errors from Google
	if errorParam != "" {
		return h.redirectWithError(writer, req, "/", errorParam, "OAuth was denied")
	}

	if code == "" || stateParam == "" {
		return h.redirectWithError(writer, req, "/", "INVALID_REQUEST", "Missing code or state parameter")
	}

	// Validate state and get redirect URI + org slug
	oauthState, err := h.svc.ValidateOAuthState(req.Context(), stateParam)
	if err != nil {
		return h.redirectWithError(writer, req, "/", "INVALID_STATE", "Invalid or expired state")
	}

	// Process OAuth callback
	result, err := h.svc.HandleCallback(req.Context(), code, oauthState.OrgSlug)
	if err != nil {
		return h.handleOAuthError(writer, req, oauthState.RedirectURI, err)
	}

	// Redirect with tokens
	redirectURL := h.buildSuccessRedirect(oauthState.RedirectURI, result)
	http.Redirect(writer, req.Request, redirectURL, http.StatusFound)

	return nil
}

// buildGoogleAuthURL constructs the Google authorization URL.
func (h *GoogleOAuthHandler) buildGoogleAuthURL(state string) string {
	params := url.Values{}
	params.Set("client_id", h.cfg.Google.ClientID)
	params.Set("redirect_uri", h.svc.getCallbackURL())
	params.Set("response_type", "code")
	params.Set("scope", "openid email profile")
	params.Set("state", state)

	return "https://accounts.google.com/o/oauth2/v2/auth?" + params.Encode()
}

// buildSuccessRedirect constructs the redirect URL with tokens.
func (h *GoogleOAuthHandler) buildSuccessRedirect(baseURI string, result *GoogleOAuthResult) string {
	parsedURL, err := url.Parse(baseURI)
	if err != nil {
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
func (h *GoogleOAuthHandler) redirectWithError(
	writer http.ResponseWriter, req bunrouter.Request,
	baseURI, code, description string,
) error {
	parsedURL, err := url.Parse(baseURI)
	if err != nil {
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
func (h *GoogleOAuthHandler) handleOAuthError(
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
		description = "Email address not verified in Google profile"
	case errors.Is(err, ErrGoogleTokenExchange):
		code = OAuthCodeTokenExchange
		description = OAuthDescTokenExchange
	default:
		code = OAuthCodeFailed
		description = fmt.Sprintf("OAuth failed: %v", err)
	}

	return h.redirectWithError(writer, req, redirectURI, code, description)
}
