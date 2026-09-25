package auth

import (
	"context"
	"log/slog"
	"net/url"
	"strings"
	"unicode"

	"github.com/fclairamb/solidping/server/internal/config"
)

// maxPostLoginRedirectLen caps a post-login redirect_uri.
//
// Dashboard deep links are short, but the MCP consent bounce carries a whole
// /api/v1/oauth/authorize request (client_id, the client's redirect_uri, PKCE
// challenge, state, resource, scope) URL-encoded inside the login page's
// returnTo — web/dash0 lib/login-destination.ts buildOAuthLoginUrl. A real one
// runs from about 500 to well over 600 characters, so a deep-link-sized cap
// (512) would silently drop every SSO sign-in started from an MCP client.
const maxPostLoginRedirectLen = 2048

// rejectedRedirectLogLen is how much of a rejected redirect_uri the WARN log
// keeps: enough to recognise a misconfigured deep link, not a free-form sink.
const rejectedRedirectLogLen = 128

// isSafePostLoginRedirect reports whether raw may be used as the destination of
// a federated login: a relative path on our own origin, and nothing else.
//
// Same rule as the MCP authorize bounce (internal/oauth/authorize.go
// redirectToLogin) and the dashboard's own guard (isSafeReturnTo): a value that
// starts with a single "/" and carries no scheme or authority is same-origin by
// construction. Absolute URLs are refused even when they name our own host —
// there is no configuration to get wrong that way.
//
// Refused: empty values, anything over maxPostLoginRedirectLen, anything not
// starting with "/", protocol-relative "//host", any backslash (browsers read
// "/\host" as "//host"), and any control character (browsers strip tab/CR/LF
// from URLs, so "/\t/host" also becomes "//host").
func isSafePostLoginRedirect(raw string) bool {
	if raw == "" || len(raw) > maxPostLoginRedirectLen {
		return false
	}

	if raw[0] != '/' || strings.HasPrefix(raw, "//") || strings.ContainsRune(raw, '\\') {
		return false
	}

	if strings.IndexFunc(raw, unicode.IsControl) != -1 {
		return false
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}

	return parsed.Scheme == "" && parsed.Host == "" && parsed.User == nil && parsed.Opaque == ""
}

// defaultPostLoginRedirect is where a federated login lands when it carried no
// usable redirect_uri: the org's dashboard home, or — for the org-less
// providers (Discord, Slack), which never had an org at login time — "/", the
// default those two logins always used.
func defaultPostLoginRedirect(orgSlug string) string {
	if orgSlug == "" {
		return "/"
	}

	return config.DashboardBasePath + "/orgs/" + url.PathEscape(orgSlug)
}

// sanitizePostLoginRedirect returns raw when it is a safe post-login
// destination (isSafePostLoginRedirect), and the default destination for
// orgSlug otherwise.
//
// Every federated login applies it twice: when the login starts, before the
// value is sealed into the OAuth state (so no state this deploy mints carries
// a foreign URL), and again when the callback reads the state back (so a state
// minted by an older deploy during a rolling upgrade is not a redirect vector
// either). Attackers mint their own login links, so the state nonce proves
// nothing about where the redirect_uri points.
//
// A rejection falls back silently for the user — the default destination is
// a working landing — and logs a WARN with the (truncated) rejected value so a
// broken deep link is diagnosable.
func sanitizePostLoginRedirect(ctx context.Context, raw, orgSlug string) string {
	if raw == "" {
		return defaultPostLoginRedirect(orgSlug)
	}

	if isSafePostLoginRedirect(raw) {
		return raw
	}

	logged := raw
	if len(logged) > rejectedRedirectLogLen {
		logged = logged[:rejectedRedirectLogLen] + "…"
	}

	slog.WarnContext(ctx, "Rejected unsafe post-login redirect_uri, using the default destination",
		"redirect_uri", logged, "redirect_uri_len", len(raw), "org_slug", orgSlug)

	return defaultPostLoginRedirect(orgSlug)
}
