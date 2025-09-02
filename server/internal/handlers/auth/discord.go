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
func (h *DiscordOAuthHandler) Login(writer http.ResponseWriter, req bunrouter.Request) error {
	redirectURI := req.URL.Query().Get("redirect_uri")
	if redirectURI == "" {
		redirectURI = "/" // Default to root
	}

	// Generate and store state
	state, err := h.svc.GenerateOAuthState(req.Context(), redirectURI)
	if err != nil {
		return h.WriteInternalError(writer, err)
	}

	// Build Discord OAuth URL
	discordAuthURL := h.buildDiscordAuthURL(state)

	http.Redirect(writer, req.Request, discordAuthURL, http.StatusFound)

	return nil
}

// Callback handles the OAuth callback from Discord.
// GET /api/v1/auth/discord/callback?code=...&state=...
func (h *DiscordOAuthHandler) Callback(writer http.ResponseWriter, req bunrouter.Request) error {
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

	// Validate state and get redirect URI
	oauthState, err := h.svc.ValidateOAuthState(req.Context(), stateParam)
	if err != nil {
		return h.redirectWithError(
			writer, req, "/", OAuthCodeInvalidState, OAuthDescInvalidState,
		)
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

// buildSuccessRedirect constructs the redirect URL with tokens.
func (h *DiscordOAuthHandler) buildSuccessRedirect(
	baseURI string, result *DiscordOAuthResult,
) string {
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
func (h *DiscordOAuthHandler) redirectWithError(
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
func (h *DiscordOAuthHandler) handleOAuthError(
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
		description = "Email address not verified in Discord profile"
	case errors.Is(err, ErrDiscordTokenExchange):
		code = OAuthCodeTokenExchange
		description = OAuthDescTokenExchange
	default:
		code = OAuthCodeFailed
		description = fmt.Sprintf("OAuth failed: %v", err)
	}

	return h.redirectWithError(writer, req, redirectURI, code, description)
}
