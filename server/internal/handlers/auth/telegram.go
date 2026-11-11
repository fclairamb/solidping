package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

const (
	telegramLoginCallbackPath = "/api/v1/auth/telegram/callback"
	telegramAuthURL           = "https://oauth.telegram.org/auth"
	telegramAuthResultParam   = "tgAuthResult"

	// OAuthCodeTelegramNotLinked is the error code of a Telegram sign-in by
	// an account no SolidPing user linked.
	OAuthCodeTelegramNotLinked = "TELEGRAM_NOT_LINKED"
	// OAuthDescTelegramNotLinked tells the user how to get past it.
	OAuthDescTelegramNotLinked = "This Telegram account is not linked to a SolidPing account. " +
		"Sign in another way, then connect Telegram in your account's notification settings."
	// OAuthCodeTelegramAmbiguous is the error code of a Telegram sign-in by an
	// account several SolidPing users linked.
	OAuthCodeTelegramAmbiguous = "TELEGRAM_AMBIGUOUS"
	// OAuthDescTelegramAmbiguous is its user-facing description.
	OAuthDescTelegramAmbiguous = "This Telegram account is linked to several SolidPing accounts. " +
		"Sign in from your organization's login page, or another way."
	// OAuthCodeTelegramExpired is the error code of a stale login result.
	OAuthCodeTelegramExpired = "TELEGRAM_AUTH_EXPIRED"
	// OAuthDescTelegramExpired is its user-facing description.
	OAuthDescTelegramExpired = "The Telegram sign-in expired, please try again"
	// OAuthCodeTelegramInvalid is the error code of a login result whose
	// signature does not verify.
	OAuthCodeTelegramInvalid = "TELEGRAM_AUTH_INVALID"
	// OAuthDescTelegramInvalid is its user-facing description.
	OAuthDescTelegramInvalid = "The Telegram sign-in could not be verified"
)

// telegramBridgeScript moves the login result from the URL fragment into the
// query string. oauth.telegram.org returns it as `#tgAuthResult=…`, and a
// fragment never reaches the server. The script is constant, so the page's
// CSP allows exactly it by hash and nothing else.
const telegramBridgeScript = `(function(){var m=location.hash.match(/[#&]tgAuthResult=([^&]*)/);` +
	`var q=new URLSearchParams(location.search);` +
	`if(m&&m[1]){q.set("tgAuthResult",decodeURIComponent(m[1]));}else{q.set("error","access_denied");}` +
	`location.replace(location.pathname+"?"+q.toString());})();`

// telegramBridgePage is served by the callback when neither the result nor an
// error is in the query string yet.
const telegramBridgePage = `<!doctype html><html><head><meta charset="utf-8">` +
	`<meta name="referrer" content="no-referrer"><title>Signing in…</title></head>` +
	`<body><noscript>Sign in with Telegram needs JavaScript.</noscript>` +
	`<script>` + telegramBridgeScript + `</script></body></html>`

// telegramBridgeCSP allows the bridge script by its hash only.
func telegramBridgeCSP() string {
	sum := sha256.Sum256([]byte(telegramBridgeScript))

	return "default-src 'none'; script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
		"'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
}

// TelegramLoginHandler handles the Sign in with Telegram endpoints.
type TelegramLoginHandler struct {
	base.HandlerBase
	svc *TelegramLoginService
	cfg *config.Config
}

// NewTelegramLoginHandler creates a new Telegram login handler.
func NewTelegramLoginHandler(service *TelegramLoginService, cfg *config.Config) *TelegramLoginHandler {
	return &TelegramLoginHandler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
		cfg:         cfg,
	}
}

// Login sends the browser to Telegram's login page.
// GET /api/v1/auth/telegram/login?org=...&redirect_uri=...
//
// org is optional: it only picks the org to sign in to when the Telegram
// account is linked in several.
func (h *TelegramLoginHandler) Login(writer http.ResponseWriter, req *http.Request) error {
	if !h.cfg.Telegram.LoginActive() {
		return redirectLoginError(writer, req, OAuthCodeFailed, "Sign in with Telegram is not enabled")
	}

	orgSlug := req.URL.Query().Get("org")
	if orgSlug != "" {
		if _, err := h.svc.db.GetOrganizationBySlug(req.Context(), orgSlug); err != nil {
			return redirectLoginError(writer, req, string(base.ErrorCodeOrganizationNotFound), "Organization not found")
		}
	}

	// Only a same-origin relative path may ride the state: the attacker mints
	// the login link, so the state nonce says nothing about this value.
	redirectURI := sanitizePostLoginRedirect(req.Context(), req.URL.Query().Get("redirect_uri"), orgSlug)

	state, err := h.svc.GenerateOAuthState(req.Context(), redirectURI, orgSlug)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	authURL, err := h.buildTelegramAuthURL(state)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	http.Redirect(writer, req, authURL, http.StatusFound)

	return nil
}

// Callback receives the browser back from Telegram.
// GET /api/v1/auth/telegram/callback?state=...[&tgAuthResult=...|&id=...&hash=...]
func (h *TelegramLoginHandler) Callback(writer http.ResponseWriter, req *http.Request) error {
	query := req.URL.Query()

	// First leg: the result is still in the fragment. Serve the bridge page
	// that re-requests this URL with it in the query string.
	if query.Get(telegramAuthResultParam) == "" && query.Get(telegramFieldHash) == "" && query.Get("error") == "" {
		return h.serveBridge(writer)
	}

	if !h.cfg.Telegram.LoginActive() {
		return h.redirectWithError(writer, req, "/", OAuthCodeFailed, "Sign in with Telegram is not enabled")
	}

	oauthState, err := h.svc.ValidateOAuthState(req.Context(), query.Get("state"))
	if err != nil {
		return h.redirectWithError(writer, req, "/", OAuthCodeInvalidState, OAuthDescInvalidState)
	}

	returnTo := sanitizePostLoginRedirect(req.Context(), oauthState.RedirectURI, oauthState.OrgSlug)

	fields, denied, err := telegramResultFields(query)
	if denied {
		return h.redirectWithError(writer, req, returnTo, "access_denied", "Telegram sign-in was cancelled")
	}

	if err != nil {
		return h.handleLoginError(writer, req, returnTo, err)
	}

	result, err := h.svc.HandleCallback(req.Context(), fields, oauthState.OrgSlug)
	if err != nil {
		return h.handleLoginError(writer, req, returnTo, err)
	}

	return finishProviderCallback(writer, req, h.svc.db, "telegram", returnTo, result)
}

// telegramResultFields extracts the signed fields from the callback query:
// either the `tgAuthResult` blob oauth.telegram.org returns, or the plain
// key/value pairs of the Login Widget's redirect form. denied reports that
// the user cancelled on Telegram's page.
func telegramResultFields(query url.Values) (map[string]string, bool, error) {
	if query.Get("error") != "" {
		return nil, true, nil
	}

	if encoded := query.Get(telegramAuthResultParam); encoded != "" {
		if encoded == "false" {
			return nil, true, nil
		}

		fields, err := DecodeTelegramAuthResult(encoded)

		return fields, false, err
	}

	fields := make(map[string]string, len(query))

	for key, values := range query {
		// state is ours, not Telegram's: it is not part of the signed data.
		if key == "state" || len(values) == 0 {
			continue
		}

		if len(values) > 1 {
			return nil, false, ErrTelegramAuthInvalid
		}

		fields[key] = values[0]
	}

	return fields, false, nil
}

// serveBridge writes the fragment-to-query bridge page.
func (h *TelegramLoginHandler) serveBridge(writer http.ResponseWriter) error {
	header := writer.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Content-Security-Policy", telegramBridgeCSP())
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Frame-Options", "DENY")
	writer.WriteHeader(http.StatusOK)

	_, err := writer.Write([]byte(telegramBridgePage))

	return err
}

// buildTelegramAuthURL constructs the oauth.telegram.org URL. Telegram checks
// origin against the domain set on the bot with @BotFather /setdomain, and
// sends the browser to return_to with the result in the fragment.
func (h *TelegramLoginHandler) buildTelegramAuthURL(state string) (string, error) {
	baseURL, err := url.Parse(h.cfg.Server.BaseURL)
	if err != nil {
		return "", err
	}

	returnTo, err := url.Parse(h.svc.getCallbackURL())
	if err != nil {
		return "", err
	}

	returnQuery := returnTo.Query()
	returnQuery.Set("state", state)
	returnTo.RawQuery = returnQuery.Encode()

	params := url.Values{}
	params.Set("bot_id", h.cfg.Telegram.BotID())
	params.Set("origin", baseURL.Scheme+"://"+baseURL.Host)
	params.Set("return_to", returnTo.String())

	return telegramAuthURL + "?" + params.Encode(), nil
}

// redirectWithError redirects with error parameters.
func (h *TelegramLoginHandler) redirectWithError(
	writer http.ResponseWriter, req *http.Request, baseURI, code, description string,
) error {
	redirectOAuthError(writer, req, baseURI, code, description)

	return nil
}

// handleLoginError maps a Telegram login failure to the error the dashboard shows.
func (h *TelegramLoginHandler) handleLoginError(
	writer http.ResponseWriter, req *http.Request, redirectURI string, err error,
) error {
	var code, description string

	switch {
	case errors.Is(err, ErrTelegramNotLinked):
		code, description = OAuthCodeTelegramNotLinked, OAuthDescTelegramNotLinked
	case errors.Is(err, ErrTelegramAmbiguous):
		code, description = OAuthCodeTelegramAmbiguous, OAuthDescTelegramAmbiguous
	case errors.Is(err, ErrTelegramAuthExpired):
		code, description = OAuthCodeTelegramExpired, OAuthDescTelegramExpired
	case errors.Is(err, ErrTelegramAuthInvalid):
		code, description = OAuthCodeTelegramInvalid, OAuthDescTelegramInvalid
	default:
		code = OAuthCodeFailed
		description = logOAuthFailure(req, "telegram", err)
	}

	return h.redirectWithError(writer, req, redirectURI, code, description)
}
