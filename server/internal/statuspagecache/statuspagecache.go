// Package statuspagecache derives the HTTP caching directives for SolidPing's
// public status-page surfaces (spec 2026-08-22-06).
//
// One helper, one answer. The page view, its summary rollup, its SVG badge,
// its Atom feed and the SPA shell served on a custom domain all read the same
// status_page row through the same visibility gate, so they must agree on who
// is allowed to store the response. Deriving the directive in a single place
// is what stops a sixth surface from quietly picking a different one.
//
// # The rule
//
//	public   → "public, max-age=<n>"  — world-readable; shared caches welcome
//	password → "private, no-store"    — never retained by anything shared
//	private  → "private, no-store"    — ditto (it 404s anyway, but fail closed)
//
// Anything that is not exactly `public` is gated. New visibilities therefore
// arrive locked rather than world-cacheable, which is the only safe default
// for a knob whose whole purpose is deciding who may read the page.
//
// `no-store` rather than `no-cache`: for a gated page the concern is a shared
// cache RETAINING the body at all, not it serving a stale one. `no-cache`
// permits storage and merely mandates revalidation — the wrong answer for a
// body somebody has to type a password to see.
//
// # Being unlocked does not make a page cacheable
//
// A valid unlock cookie authorizes THIS visitor. The CDN or corporate proxy in
// front of them holds no such cookie and serves everybody. So the directive is
// keyed on the page's visibility alone and never on whether the caller got in:
// an unlocked password page is still `private, no-store`.
package statuspagecache

import (
	"net/http"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Gated is the directive for every page that is not world-readable, and for
// the 401/404 answers on the public surface — a cacheable "not found" is an
// existence oracle a shared cache would happily re-serve.
const Gated = "private, no-store"

// Vary enumerates the request headers a public status-page response depends
// on. Both entries are real, and the list is deliberately exhaustive so that
// adding a request-derived field to any of these payloads without extending it
// fails the pinning test rather than shipping a cross-visitor leak:
//
//   - Cookie — the host-only unlock cookie decides what a visitor of a
//     `password` page may see. Those responses are `no-store` today, so this is
//     belt-and-braces; it stays listed because a page's visibility can change
//     under a cache that already holds an entry for the URL.
//   - X-Forwarded-Proto — the summary payload embeds an absolute page URL whose
//     scheme comes from this header (see statuspages.requestScheme). The Host
//     half of that URL needs no Vary: it is part of every cache key already.
//
// Notably absent: Accept-Language. A status page renders in the language
// stored on the page row, not the one the browser asks for, so the response
// does not vary by it.
const Vary = "Cookie, X-Forwarded-Proto"

// PageMaxAge is the freshness budget for the page, summary and badge. Sixty
// seconds is what the summary and badge already promised, and it matches the
// aggregation cadence — a status page that keeps claiming "all systems
// operational" for longer than that after an incident opens is the product
// failing at its one job.
const PageMaxAge = 60 * time.Second

// FeedMaxAge is the Atom feed's budget, unchanged from what it already sent: a
// feed reader polls on its own schedule and a status-update timeline moves far
// more slowly than a rollup.
const FeedMaxAge = 5 * time.Minute

// Control returns the Cache-Control value for a public status-page response
// serving a page of this visibility.
func Control(visibility string, maxAge time.Duration) string {
	if visibility != models.StatusPageVisibilityPublic {
		return Gated
	}

	return "public, max-age=" + strconv.Itoa(int(maxAge.Seconds()))
}

// Apply writes Cache-Control and Vary onto a public status-page response.
func Apply(header http.Header, visibility string, maxAge time.Duration) {
	header.Set("Cache-Control", Control(visibility, maxAge))
	header.Set("Vary", Vary)
}

// ApplyGated writes the never-shared directive. Used where no page is in hand
// — the 404/401 answers — so a shared cache cannot turn the public surface's
// error replies into a map of which pages exist.
func ApplyGated(header http.Header) {
	header.Set("Cache-Control", Gated)
	header.Set("Vary", Vary)
}
