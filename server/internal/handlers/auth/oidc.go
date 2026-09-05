package auth

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// OIDCOAuthHandler handles generic OpenID Connect authentication endpoints.
type OIDCOAuthHandler struct {
	base.HandlerBase
	svc *OIDCOAuthService
	cfg *config.Config
}

// NewOIDCOAuthHandler creates a new generic OIDC handler.
func NewOIDCOAuthHandler(service *OIDCOAuthService, cfg *config.Config) *OIDCOAuthHandler {
	return &OIDCOAuthHandler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
		cfg:         cfg,
	}
}

// Login initiates the generic OIDC flow.
// GET /api/v1/auth/oidc/login?org=...&redirect_uri=...
func (h *OIDCOAuthHandler) Login(writer http.ResponseWriter, req *http.Request) error {
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

	oauth2Cfg, err := h.svc.oauth2Config(req.Context())
	if err != nil {
		if errors.Is(err, ErrOIDCNotConfigured) {
			return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "OIDC provider is not configured")
		}

		return h.WriteInternalError(writer, req, err)
	}

	// Generate and store state with org slug
	state, err := h.svc.GenerateOAuthState(req.Context(), redirectURI, orgSlug)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	authURL := oauth2Cfg.AuthCodeURL(state)

	http.Redirect(writer, req, authURL, http.StatusFound)

	return nil
}

// Callback handles the OAuth callback from the generic OIDC provider.
// GET /api/v1/auth/oidc/callback?code=...&state=...
func (h *OIDCOAuthHandler) Callback(writer http.ResponseWriter, req *http.Request) error {
	code := req.URL.Query().Get("code")
	stateParam := req.URL.Query().Get("state")
	errorParam := req.URL.Query().Get("error")

	// Handle errors from the IdP
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

	// Redirect with tokens. Also set the SPA session cookie so
	// cookie-authenticated surfaces (the embedded MCP OAuth
	// authorize/consent flow) work without a login-page refresh bounce.
	return finishProviderCallback(writer, req,
		h.buildSuccessRedirect(oauthState.RedirectURI, result),
		result.PendingOrgSlug, result.AccessToken, result.ExpiresIn, result.Pending)
}

// buildSuccessRedirect constructs the redirect URL with tokens.
func (h *OIDCOAuthHandler) buildSuccessRedirect(baseURI string, result *OIDCOAuthResult) string {
	parsedURL, err := url.Parse(baseURI)
	if err != nil {
		parsedURL, _ = url.Parse("/")
	}

	query := parsedURL.Query()
	query.Set("access_token", result.AccessToken)
	query.Set("refresh_token", result.RefreshToken)
	query.Set("expires_in", strconv.Itoa(result.ExpiresIn))
	query.Set("org", result.OrgSlug)
	parsedURL.RawQuery = query.Encode()

	return parsedURL.String()
}

// redirectWithError redirects with error parameters.
func (h *OIDCOAuthHandler) redirectWithError(
	writer http.ResponseWriter, req *http.Request,
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

	http.Redirect(writer, req, parsedURL.String(), http.StatusFound)

	return nil
}

// handleOAuthError handles OAuth errors by redirecting with error information.
func (h *OIDCOAuthHandler) handleOAuthError(
	writer http.ResponseWriter, req *http.Request,
	redirectURI string, err error,
) error {
	var code, description string

	switch {
	case errors.Is(err, ErrInvalidOAuthState):
		code = OAuthCodeInvalidState
		description = OAuthDescInvalidState
	case errors.Is(err, ErrEmailNotVerified):
		code = OAuthCodeEmailNotVerified
		description = "Email address not verified with the OIDC provider"
	case errors.Is(err, ErrOIDCTokenExchange):
		code = OAuthCodeTokenExchange
		description = OAuthDescTokenExchange
	case errors.Is(err, ErrOIDCTokenInvalid):
		code = "TOKEN_INVALID"
		description = "ID token failed validation"
	case errors.Is(err, ErrOIDCNotConfigured):
		code = "NOT_CONFIGURED"
		description = "OIDC provider is not configured"
	default:
		code = OAuthCodeFailed
		description = logOAuthFailure(req, "oidc", err)
	}

	return h.redirectWithError(writer, req, redirectURI, code, description)
}
