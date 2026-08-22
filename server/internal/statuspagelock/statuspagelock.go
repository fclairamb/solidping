// Package statuspagelock implements the unlock mechanism for
// password-protected status pages (spec 2026-08-21-07).
//
// A status page has three visibilities. `public` is world-readable, `private`
// is fully hidden (the public endpoints answer 404, as if the page did not
// exist), and `password` is shared-with-a-secret: the page is known to exist,
// but every public read answers 401 until the visitor proves they hold the
// password.
//
// # The unlock cookie
//
// Proof is a cookie, not a bearer token in a header, because the things that
// must carry it are an <img> for the badge, a feed reader following
// feed.xml, and a plain browser navigation — none of which can set headers.
//
// The cookie is HOST-ONLY on purpose: it is issued with no Domain attribute,
// so the browser scopes it to the exact host that served the unlock. That is
// what makes it work on a custom domain (status.acme.com gets its own cookie)
// without ever minting a cookie for solidping.io that a customer's page could
// then present elsewhere.
//
// # Why the password hash is the HMAC key
//
// The token is `<expiry>.<base64 HMAC>` keyed by sha256(password hash). Two
// properties fall out for free, with no new server secret, no revocation list
// and no extra column:
//
//   - Changing (or clearing) the password invalidates every outstanding
//     cookie instantly, because the key that signed them no longer exists.
//   - A cookie minted for one page can never unlock another, because the page
//     UID is inside the signed payload AND each page has its own key.
//
// The hash never leaves the server — only an HMAC derived from it does — so
// this does not weaken the stored credential.
package statuspagelock

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrLocked is returned by service code that resolved a password-protected
// page for a caller holding no valid unlock. Handlers translate it to 401 with
// the STATUS_PAGE_LOCKED code; it must never be conflated with "not found",
// which is what `private` gets.
var ErrLocked = errors.New("status page is locked")

// TTL is how long an unlock lasts. A working day: long enough that a customer
// following an incident does not have to re-enter the password every hour,
// short enough that a shared laptop does not stay unlocked for a week.
const TTL = 12 * time.Hour

// CookiePrefix namespaces the per-page cookies. The page UID is appended, so a
// visitor may hold unlocks for several pages on the same host at once — which
// is the normal case for an org serving many pages under one domain.
const CookiePrefix = "sp_unlock_"

// CookieName returns the cookie name carrying the unlock for one page.
func CookieName(pageUID string) string { return CookiePrefix + pageUID }

// Protected reports whether the page is password-gated. A page whose
// visibility says `password` but which carries no hash is NOT protected — and
// is not servable either; see Visible.
func Protected(page *models.StatusPage) bool {
	return page != nil &&
		page.Visibility == models.StatusPageVisibilityPassword &&
		page.PasswordHash != nil && *page.PasswordHash != ""
}

// Visible reports whether the page may be served on a public surface at all,
// before any unlock is considered.
//
// `password` counts as visible: the point of the mode is that the page exists
// and can be unlocked, unlike `private`, which 404s. The one exception is a
// `password` page with NO stored hash — an inconsistent row that could only
// arise from a hand-edited database or a down-migration that dropped the hash
// column. It is treated as not visible rather than as public: failing closed on
// a page whose gate has gone missing is the only safe reading.
func Visible(page *models.StatusPage) bool {
	if page == nil || !page.Enabled {
		return false
	}

	switch page.Visibility {
	case models.StatusPageVisibilityPublic:
		return true
	case models.StatusPageVisibilityPassword:
		return page.PasswordHash != nil && *page.PasswordHash != ""
	default:
		return false
	}
}

// key derives the per-page HMAC key from the stored password hash.
func key(passwordHash string) []byte {
	sum := sha256.Sum256([]byte(passwordHash))

	return sum[:]
}

// sign computes the signature over "<pageUID>.<expiry>".
func sign(passwordHash, pageUID string, expiry int64) string {
	mac := hmac.New(sha256.New, key(passwordHash))
	mac.Write([]byte(pageUID + "." + strconv.FormatInt(expiry, 10)))

	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Issue mints an unlock token valid for ttl. The expiry travels in the clear
// inside the token AND is covered by the signature, so a client cannot extend
// its own unlock by editing the cookie.
func Issue(passwordHash, pageUID string, now time.Time, ttl time.Duration) string {
	expiry := now.Add(ttl).Unix()

	return strconv.FormatInt(expiry, 10) + "." + sign(passwordHash, pageUID, expiry)
}

// Valid reports whether token is a live unlock for this page under this
// password hash. Everything about it is checked in constant time where it
// matters: the signature comparison uses hmac.Equal.
func Valid(passwordHash, pageUID, token string, now time.Time) bool {
	if passwordHash == "" || token == "" {
		return false
	}

	rawExpiry, signature, found := strings.Cut(token, ".")
	if !found {
		return false
	}

	expiry, err := strconv.ParseInt(rawExpiry, 10, 64)
	if err != nil {
		return false
	}

	if now.Unix() >= expiry {
		return false
	}

	return hmac.Equal([]byte(signature), []byte(sign(passwordHash, pageUID, expiry)))
}

// RequestUnlocks reports whether an inbound request carries a valid unlock
// cookie for the page. A page that is not protected is trivially unlocked.
func RequestUnlocks(req *http.Request, page *models.StatusPage) bool {
	if !Protected(page) {
		return true
	}

	if req == nil {
		return false
	}

	cookie, err := req.Cookie(CookieName(page.UID))
	if err != nil {
		return false
	}

	return Valid(*page.PasswordHash, page.UID, cookie.Value, time.Now())
}

// SetCookie writes the unlock cookie onto the response.
//
// No Domain attribute — the cookie is deliberately host-only (see the package
// doc). Secure is set only when the request actually arrived over TLS, because
// a Secure cookie on a plain-HTTP dev instance is silently dropped by the
// browser and the unlock would appear to do nothing.
func SetCookie(writer http.ResponseWriter, req *http.Request, pageUID, token string) {
	http.SetCookie(writer, &http.Cookie{
		Name:     CookieName(pageUID),
		Value:    token,
		Path:     "/",
		MaxAge:   int(TTL.Seconds()),
		HttpOnly: true,
		Secure:   isTLS(req),
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearCookie expires the unlock cookie for a page. Used when the password is
// wrong so a stale/forged cookie does not linger.
func ClearCookie(writer http.ResponseWriter, req *http.Request, pageUID string) {
	http.SetCookie(writer, &http.Cookie{
		Name:     CookieName(pageUID),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isTLS(req),
		SameSite: http.SameSiteLaxMode,
	})
}

// isTLS reports whether the request reached us over HTTPS, honouring the
// X-Forwarded-Proto header set by the edge proxy that terminates TLS for
// custom domains.
func isTLS(req *http.Request) bool {
	if req == nil {
		return false
	}

	if req.TLS != nil {
		return true
	}

	return strings.EqualFold(req.Header.Get("X-Forwarded-Proto"), "https")
}

// --- Context grant ---

// Grant answers "may this caller see this password-protected page?". It is
// carried in the request context so the service layer can enforce the gate at
// the same place it enforces visibility, without every service method growing
// an *http.Request parameter it would otherwise have no use for.
type Grant func(page *models.StatusPage) bool

type grantKey struct{}

// WithGrant returns a context carrying the access decision for password pages.
func WithGrant(ctx context.Context, grant Grant) context.Context {
	return context.WithValue(ctx, grantKey{}, grant)
}

// FromRequest builds the grant for one inbound HTTP request.
func FromRequest(req *http.Request) Grant {
	return func(page *models.StatusPage) bool { return RequestUnlocks(req, page) }
}

// WithRequestGrant installs the request's own unlock grant on its context.
// The router middleware that mounts this on the public API surface lives in
// the app package (it has to speak httpx.Middleware); anything reached WITHOUT
// it — the MCP tools, a background job, a unit test — gets the
// DENY-BY-DEFAULT behaviour of Allows below, which is the correct answer for a
// caller that cannot present a visitor's cookie.
func WithRequestGrant(req *http.Request) *http.Request {
	return req.WithContext(WithGrant(req.Context(), FromRequest(req)))
}

// Allows reports whether the caller behind ctx may read this page.
//
// A page that is not password-protected is always allowed. A protected page
// needs a grant in the context that says yes; NO GRANT MEANS NO, so forgetting
// to mount the middleware locks pages rather than exposing them.
func Allows(ctx context.Context, page *models.StatusPage) bool {
	if !Protected(page) {
		return true
	}

	grant, ok := ctx.Value(grantKey{}).(Grant)
	if !ok || grant == nil {
		return false
	}

	return grant(page)
}
