// Package statuspagekiosk implements the kiosk-token grant for SolidPing's
// public status-page surfaces (spec 2026-08-29-08).
//
// # Why a third access mode exists
//
// A status page on an office TV has to render unattended, for months, with
// nobody at a keyboard. Neither existing mode can do that:
//
//   - `password` issues a 12 h unlock cookie (internal/statuspagelock), so
//     somebody re-types the password on the wallboard every morning;
//   - `private` answers 404 on every public endpoint, by design.
//
// The kiosk token is the third answer: one long-lived, revocable, per-page
// secret that grants READ-ONLY view of exactly one page. It bypasses the
// password lock and turns `private` into "unlisted for this one screen". It
// grants nothing else — no write, no admin, no other page.
//
// # No oracle
//
// The single most important property here is that a WRONG or REVOKED token
// behaves EXACTLY like no token at all: a password page still answers 401
// STATUS_PAGE_LOCKED, a private page still answers 404. Nothing in this
// package returns a distinguishable error, logs the token, or varies a header
// on a failed match — Decide collapses every failure into the same two
// outcomes the tokenless path already produces. A "bad kiosk token" reply
// would otherwise confirm that the page exists, which is precisely what
// `private` is bought to hide.
//
// # Why sha256 and not argon2id
//
// The stored value is sha256 hex of a 32-byte CSPRNG token. PasswordHash uses
// argon2id because a human chose that secret and a dictionary attack is real;
// here there is no dictionary — guessing the token means guessing 256 bits —
// and a wallboard re-presents it every 15-30 s, so a memory-hard verification
// per request would cost a great deal and buy nothing.
package statuspagekiosk

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
)

// QueryParam is the query parameter carrying the token. A query parameter
// rather than a header because the thing presenting it is a browser opening a
// URL typed into a TV stick — it cannot set headers. The SPA strips it from
// the address bar after load (history.replaceState) so it is not burned onto
// the screen for every passer-by.
const QueryParam = "kiosk"

// tokenBytes is the entropy behind a kiosk token. 32 bytes / 256 bits: this is
// a bearer credential with no rate limiter in front of it (a wallboard polls
// constantly, so throttling it would break the feature), so brute force has to
// be answered by entropy alone.
const tokenBytes = 32

// Generate mints a new kiosk token, returning the plaintext to show the
// operator ONCE and the hash to store. The plaintext is never persisted, never
// logged and never re-derivable — regenerating is the only way to get one.
func Generate() (string, string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}

	token := base64.RawURLEncoding.EncodeToString(raw)

	return token, Hash(token), nil
}

// Hash returns the storage form of a token.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))

	return hex.EncodeToString(sum[:])
}

// Valid reports whether token matches the stored hash. Empty on either side is
// always false — a page with no token must not be unlocked by an empty
// `?kiosk=` — and the comparison is constant-time.
func Valid(storedHash, token string) bool {
	if storedHash == "" || token == "" {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(Hash(token)), []byte(storedHash)) == 1
}

// Holds reports whether the page carries a kiosk token this request satisfies.
func Holds(page *models.StatusPage, token string) bool {
	if page == nil || page.KioskTokenHash == nil {
		return false
	}

	return Valid(*page.KioskTokenHash, token)
}

// --- Context grant ---

// Grant answers "does this caller hold this page's kiosk token?". It mirrors
// statuspagelock.Grant: carried on the request context so the service layer
// enforces it next to the visibility gate, without every service method
// growing an *http.Request parameter.
type Grant func(page *models.StatusPage) bool

type grantKey struct{}

// WithGrant returns a context carrying the kiosk decision.
func WithGrant(ctx context.Context, grant Grant) context.Context {
	return context.WithValue(ctx, grantKey{}, grant)
}

// FromRequest builds the kiosk grant for one inbound HTTP request.
func FromRequest(req *http.Request) Grant {
	token := ""
	if req != nil && req.URL != nil {
		token = req.URL.Query().Get(QueryParam)
	}

	return func(page *models.StatusPage) bool { return Holds(page, token) }
}

// WithRequestGrant installs the request's own kiosk grant on its context.
// Anything reached WITHOUT it — the MCP tools, a background job, a unit test —
// gets the deny-by-default behavior of Allows, which is the correct answer
// for a caller that presented no token.
func WithRequestGrant(req *http.Request) *http.Request {
	return req.WithContext(WithGrant(req.Context(), FromRequest(req)))
}

// Allows reports whether the caller behind ctx holds this page's kiosk token.
// NO GRANT MEANS NO, so forgetting to mount the middleware locks pages rather
// than exposing them.
func Allows(ctx context.Context, page *models.StatusPage) bool {
	grant, ok := ctx.Value(grantKey{}).(Grant)
	if !ok || grant == nil {
		return false
	}

	return grant(page)
}

// --- The gate ---

// Decision is the outcome of the public access gate for one page.
type Decision int

// Decision values.
const (
	// DecisionAllow means the caller may read the page.
	DecisionAllow Decision = iota
	// DecisionNotFound means the caller must be told the page does not exist:
	// it is disabled, `private` without a valid kiosk token, or an
	// inconsistent `password` row with no hash. Callers map it to their own
	// ErrStatusPageNotFound.
	DecisionNotFound
	// DecisionLocked means the page exists and is password-gated, and the
	// caller holds neither an unlock cookie nor a kiosk token. Callers map it
	// to statuspagelock.ErrLocked (401 STATUS_PAGE_LOCKED).
	DecisionLocked
)

// Decide is the SINGLE public-access gate for a status page: every public
// surface (page view, summary, badge, incident history) calls it, so they can
// never drift apart on who may read what.
//
// Order matters and is deliberate:
//
//  1. A page that is missing or DISABLED is not found — no token overrides
//     that. Disabling a page is an operator saying "stop serving this", and a
//     kiosk screen is exactly the audience that must stop seeing it.
//  2. A valid kiosk token allows everything below. This is what turns
//     `private` into "unlisted for this one screen" and skips the password.
//  3. Otherwise the ordinary visibility gate applies, unchanged.
//
// Because step 2 is a plain boolean, an invalid or revoked token produces
// byte-identical behavior to no token at all.
func Decide(ctx context.Context, page *models.StatusPage) Decision {
	if page == nil || !page.Enabled {
		return DecisionNotFound
	}

	if Allows(ctx, page) {
		return DecisionAllow
	}

	if !statuspagelock.Visible(page) {
		return DecisionNotFound
	}

	if !statuspagelock.Allows(ctx, page) {
		return DecisionLocked
	}

	return DecisionAllow
}
