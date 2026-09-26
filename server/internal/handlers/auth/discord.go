package auth

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// DiscordOAuthHandler handles Discord OAuth authentication endpoints.
type DiscordOAuthHandler struct {
	base.HandlerBase
	svc *DiscordOAuthService
	cfg *config.Config
}

// NewDiscordOAuthHandler creates a new Discord OAuth handler.
func NewDiscordOAuthHandler(
	service *DiscordOAuthService, cfg *config.Config,
) *DiscordOAuthHandler {
	return &DiscordOAuthHandler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
		cfg:         cfg,
	}
}

// Login initiates the Discord OAuth flow.
// GET /api/v1/auth/discord/login?redirect_uri=...
func (h *DiscordOAuthHandler) Login(writer http.ResponseWriter, req *http.Request) error {
	// Only a same-origin relative path may ride the state: the attacker mints
	// the login link, so the state nonce says nothing about this value. There
	// is no org at this point, so the default is "/".
	redirectURI := sanitizePostLoginRedirect(req.Context(), req.URL.Query().Get("redirect_uri"), "")

	// Generate and store state
	state, err := h.svc.GenerateOAuthState(req.Context(), redirectURI)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	// Build Discord OAuth URL
	discordAuthURL := h.buildDiscordAuthURL(state)

	http.Redirect(writer, req, discordAuthURL, http.StatusFound)

	return nil
}

// Callback handles the OAuth callback from Discord.
// GET /api/v1/auth/discord/callback?code=...&state=...
func (h *DiscordOAuthHandler) Callback(writer http.ResponseWriter, req *http.Request) error {
	code := req.URL.Query().Get("code")
	stateParam := req.URL.Query().Get("state")
	errorParam := req.URL.Query().Get("error")

	// Handle errors from Discord
	if errorParam != "" {
		return h.redirectWithError(writer, req, "/", errorParam, "OAuth was denied")
	}

	if code == "" || stateParam == "" {
		return h.redirectWithError(
			writer, req, "/", "INVALID_REQUEST", "Missing code or state parameter",
		)
	}

	// A `link:`-prefixed state is a LINK round trip, not a login. It gets its
	// own handler that mints no session and creates no organization — see
	// discord_link.go for why that separation is structural rather than a flag.
	if isDiscordLinkState(stateParam) {
		return h.handleLinkCallback(writer, req, code, stateParam)
	}

	// Validate state and get redirect URI
	oauthState, err := h.svc.ValidateOAuthState(req.Context(), stateParam)
	if err != nil {
		return h.redirectWithError(
			writer, req, "/", OAuthCodeInvalidState, OAuthDescInvalidState,
		)
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
	return finishProviderCallback(writer, req, h.svc.db, "discord", returnTo, result)
}

// buildDiscordAuthURL constructs the Discord authorization URL.
func (h *DiscordOAuthHandler) buildDiscordAuthURL(state string) string {
	params := url.Values{}
	params.Set("client_id", h.cfg.Discord.ClientID)
	params.Set("redirect_uri", h.svc.getCallbackURL())
	params.Set("response_type", "code")
	params.Set("scope", "identify email guilds")
	params.Set("state", state)

	return "https://discord.com/oauth2/authorize?" + params.Encode()
}

// redirectWithError redirects with error parameters. The destination goes
// through redirectOAuthError's same-origin guard.
func (h *DiscordOAuthHandler) redirectWithError(
	writer http.ResponseWriter, req *http.Request,
	baseURI, code, description string,
) error {
	redirectOAuthError(writer, req, baseURI, code, description)

	return nil
}

// handleOAuthError handles OAuth errors by redirecting with error information.
func (h *DiscordOAuthHandler) handleOAuthError(
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
		description = "Email address not verified in Discord profile"
	case errors.Is(err, ErrDiscordTokenExchange):
		code = OAuthCodeTokenExchange
		description = OAuthDescTokenExchange
	default:
		code = OAuthCodeFailed
		description = logOAuthFailure(req, "discord", err)
	}

	return h.redirectWithError(writer, req, redirectURI, code, description)
}
