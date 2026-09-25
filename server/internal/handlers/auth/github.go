package auth

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// GitHubOAuthHandler handles GitHub OAuth authentication endpoints.
type GitHubOAuthHandler struct {
	base.HandlerBase
	svc *GitHubOAuthService
	cfg *config.Config
}

// NewGitHubOAuthHandler creates a new GitHub OAuth handler.
func NewGitHubOAuthHandler(service *GitHubOAuthService, cfg *config.Config) *GitHubOAuthHandler {
	return &GitHubOAuthHandler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
		cfg:         cfg,
	}
}

// Login initiates the GitHub OAuth flow.
// GET /api/v1/auth/github/login?org=...&redirect_uri=...
func (h *GitHubOAuthHandler) Login(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := req.URL.Query().Get("org")
	if orgSlug == "" {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "org parameter is required")
	}

	// Validate that the org exists
	if _, err := h.svc.db.GetOrganizationBySlug(req.Context(), orgSlug); err != nil {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	}

	// Only a same-origin relative path may ride the state: the attacker mints
	// the login link, so the state nonce says nothing about this value.
	redirectURI := sanitizePostLoginRedirect(req.Context(), req.URL.Query().Get("redirect_uri"), orgSlug)

	// Generate and store state with org slug
	state, err := h.svc.GenerateOAuthState(req.Context(), redirectURI, orgSlug)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	// Build GitHub OAuth URL
	gitHubAuthURL := h.buildGitHubAuthURL(state)

	http.Redirect(writer, req, gitHubAuthURL, http.StatusFound)

	return nil
}

// Callback handles the OAuth callback from GitHub.
// GET /api/v1/auth/github/callback?code=...&state=...
func (h *GitHubOAuthHandler) Callback(writer http.ResponseWriter, req *http.Request) error {
	code := req.URL.Query().Get("code")
	stateParam := req.URL.Query().Get("state")
	errorParam := req.URL.Query().Get("error")

	// Handle errors from GitHub
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

	// Re-check the redirect the state carries: a state minted before this
	// guard existed (rolling upgrade) must not become a redirect vector.
	returnTo := sanitizePostLoginRedirect(req.Context(), oauthState.RedirectURI, oauthState.OrgSlug)

	// Process OAuth callback
	result, err := h.svc.HandleCallback(req.Context(), code, oauthState.OrgSlug)
	if err != nil {
		return h.handleOAuthError(writer, req, returnTo, err)
	}

	// Hand the session to the dashboard through a single-use code: the
	// tokens never appear in the redirect URL (spec 2026-09-25-12).
	return finishProviderCallback(writer, req, h.svc.db, "github", returnTo, result)
}

// buildGitHubAuthURL constructs the GitHub authorization URL.
func (h *GitHubOAuthHandler) buildGitHubAuthURL(state string) string {
	params := url.Values{}
	params.Set("client_id", h.cfg.GitHub.ClientID)
	params.Set("redirect_uri", h.svc.getCallbackURL())
	params.Set("scope", "user:email")
	params.Set("state", state)

	return "https://github.com/login/oauth/authorize?" + params.Encode()
}

// redirectWithError redirects with error parameters. The destination goes
// through redirectOAuthError's same-origin guard.
func (h *GitHubOAuthHandler) redirectWithError(
	writer http.ResponseWriter, req *http.Request,
	baseURI, code, description string,
) error {
	redirectOAuthError(writer, req, baseURI, code, description)

	return nil
}

// handleOAuthError handles OAuth errors by redirecting with error information.
func (h *GitHubOAuthHandler) handleOAuthError(
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
		description = "No verified email found in GitHub profile"
	case errors.Is(err, ErrGitHubTokenExchange):
		code = OAuthCodeTokenExchange
		description = OAuthDescTokenExchange
	default:
		code = OAuthCodeFailed
		description = logOAuthFailure(req, "github", err)
	}

	return h.redirectWithError(writer, req, redirectURI, code, description)
}
