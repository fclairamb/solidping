package auth

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// GitLabOAuthHandler handles GitLab OAuth authentication endpoints.
type GitLabOAuthHandler struct {
	base.HandlerBase
	svc *GitLabOAuthService
	cfg *config.Config
}

// NewGitLabOAuthHandler creates a new GitLab OAuth handler.
func NewGitLabOAuthHandler(service *GitLabOAuthService, cfg *config.Config) *GitLabOAuthHandler {
	return &GitLabOAuthHandler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
		cfg:         cfg,
	}
}

// Login initiates the GitLab OAuth flow.
// GET /api/v1/auth/gitlab/login?org=...&redirect_uri=...
func (h *GitLabOAuthHandler) Login(writer http.ResponseWriter, req *http.Request) error {
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

	// Build GitLab OAuth URL
	gitLabAuthURL := h.buildGitLabAuthURL(state)

	http.Redirect(writer, req, gitLabAuthURL, http.StatusFound)

	return nil
}

// Callback handles the OAuth callback from GitLab.
// GET /api/v1/auth/gitlab/callback?code=...&state=...
func (h *GitLabOAuthHandler) Callback(writer http.ResponseWriter, req *http.Request) error {
	code := req.URL.Query().Get("code")
	stateParam := req.URL.Query().Get("state")
	errorParam := req.URL.Query().Get("error")

	// Handle errors from GitLab
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
	return finishProviderCallback(writer, req, h.svc.db, "gitlab", returnTo, result)
}

// buildGitLabAuthURL constructs the GitLab authorization URL.
func (h *GitLabOAuthHandler) buildGitLabAuthURL(state string) string {
	params := url.Values{}
	params.Set("client_id", h.cfg.GitLab.ClientID)
	params.Set("redirect_uri", h.svc.getCallbackURL())
	params.Set("response_type", "code")
	params.Set("scope", "read_user openid email")
	params.Set("state", state)

	return h.svc.getGitLabBaseURL() + "/oauth/authorize?" + params.Encode()
}

// redirectWithError redirects with error parameters. The destination goes
// through redirectOAuthError's same-origin guard.
func (h *GitLabOAuthHandler) redirectWithError(
	writer http.ResponseWriter, req *http.Request,
	baseURI, code, description string,
) error {
	redirectOAuthError(writer, req, baseURI, code, description)

	return nil
}

// handleOAuthError handles OAuth errors by redirecting with error information.
func (h *GitLabOAuthHandler) handleOAuthError(
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
		description = "No verified email found in GitLab profile"
	case errors.Is(err, ErrGitLabTokenExchange):
		code = OAuthCodeTokenExchange
		description = OAuthDescTokenExchange
	default:
		code = OAuthCodeFailed
		description = logOAuthFailure(req, "gitlab", err)
	}

	return h.redirectWithError(writer, req, redirectURI, code, description)
}
