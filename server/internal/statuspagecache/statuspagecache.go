// Package statuspagecache derives the HTTP caching directives for SolidPing's
// public status-page surfaces (spec 2026-08-22-06).
//
// One helper, one answer. The page view, its summary rollup, its SVG badge,
// its public incident history, its Atom feed and the two status0 SPA shells
// (path-based and custom-domain) all read the same status_page row through the
// same visibility gate, so they must agree on who is allowed to store the
// response. Deriving the directive in a single place is what stops the next
// surface from quietly picking a different one.
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
//
// # Two freshness numbers, declared together
//
// A reader's page can be stale for two independent reasons: the cache in front
// of them is holding a copy (PageMaxAge, plus the PageStaleWhileRevalidate
// grace window), and the server reused a computed view rather than recomputing
// it (PageMemoTTL, spec 2026-09-22-09). Whoever changes one has to see the
// other, so both live here rather than one of them next to the code that
// implements the memo — a 15 s server-side reuse window under a 60 s browser
// copy adds nothing a reader could notice, and that claim only survives if the
// two numbers cannot drift apart unnoticed.
//
// stale-while-revalidate is on the PUBLIC directive only. A gated page answers
// `private, no-store`, and a grace window on a body nothing may store is
// meaningless; the pinning tests assert the absence.
//
// # Accept-Encoding is not this package's Vary
//
// Exactly one request header changes such a body per the reasoning above
// (X-Forwarded-Proto). That is still true after the process-wide gzip wrapper
// (server.compression, internal/app's compressionWrapper) was added: the
// wrapper sits OUTSIDE every handler in this package and appends
// Accept-Encoding to Vary itself, on the response this package already wrote.
// The handler-level VaryPublic/VaryGated constants below are unchanged and
// stay exactly what the pinning tests assert — the transport layer's
// Accept-Encoding is a separate, additive fact about the wire representation,
// not a new source of body variation this package needs to account for.
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

// VaryPublic is the Vary header on a world-readable status-page response.
//
// Exactly one request header changes such a body: X-Forwarded-Proto. The
// summary payload embeds an absolute page URL whose scheme comes from it (see
// statuspages.requestScheme), and the SPA shell injects the same origin into
// its og:url. The Host half of those URLs needs no Vary — it is part of every
// cache key already.
//
// Notably ABSENT, and deliberately so:
//
//   - Cookie. A public page's body does not depend on any cookie, and listing
//     it would be actively harmful: Cloudflare, Fastly and Varnish all refuse
//     to cache (or key uselessly on) a response carrying Vary: Cookie, which
//     would leave this spec's whole point — shared caches absorbing the spike
//     that arrives while the infrastructure is already unhealthy — unbuilt.
//     Vary is also the wrong tool for the visibility-flip worry: it does not
//     invalidate anything when the origin changes, so a same-cookie request
//     would still be served the stale public copy. What bounds that window is
//     max-age, not Vary.
//   - Accept-Language. A status page renders in the language stored on the
//     page row, not the one the browser asks for.
//
// Extending this list is what a new request-derived field in any of these
// payloads requires; the pinning tests fail loudly if one lands without it.
const VaryPublic = "X-Forwarded-Proto"

// VaryGated is the Vary header on a gated response. It adds Cookie, where the
// header is genuinely correct: the host-only unlock cookie is what decides
// whether a `password` page renders or 401s. Belt and braces next to
// `no-store` — nothing should be storing these at all — but the two travel
// together so a future relaxation of the directive cannot silently drop it.
const VaryGated = "Cookie, " + VaryPublic

// PageMaxAge is the freshness budget for the page, summary and badge. Sixty
// seconds is what the summary and badge already promised, and it matches the
// aggregation cadence — a status page that keeps claiming "all systems
// operational" for longer than that after an incident opens is the product
// failing at its one job.
const PageMaxAge = 60 * time.Second

// PageMemoTTL is how long the SERVER may reuse a computed public status-page
// view before recomputing it (spec 2026-09-22-09). It sits here, next to
// PageMaxAge, because the two are one freshness story told at two layers and
// reading either one alone gives the wrong answer about how stale a reader's
// page can be.
//
// Fifteen seconds is a quarter of PageMaxAge, so the memo can never add
// staleness a reader could notice on top of what the HTTP directive already
// permits: a browser holding a 60 s copy is the binding constraint, and the
// server-side reuse window disappears inside it. It is also half the fastest
// poll any SolidPing surface makes (TV mode's 30 s status refresh), so a
// wallboard still sees a fresh computation on every other tick.
//
// Not configurable, deliberately. A knob here is a knob on how out of date a
// status page may be, which is a product decision rather than a deployment
// one; the invalidation list (statuspages.PageMemoWritePaths) is what makes an
// operator's own edit visible immediately regardless of this number.
const PageMemoTTL = 15 * time.Second

// PageStaleWhileRevalidate is the grace window a shared or browser cache may
// serve an EXPIRED public page for while it refreshes in the background
// (spec 2026-09-22-09).
//
// It exists because the alternative is worse than staleness: with max-age
// alone, the reader whose copy expired one second before an incident spike
// blocks on a cold origin render — the very moment the page matters most and
// the infrastructure behind it is least able to answer. Thirty seconds of
// "show the old number, fetch the new one" turns that block into a background
// fetch, and the server-side memo above makes that fetch cheap.
//
// Worst-case staleness for a reader polling every 30 s therefore rises from
// 60 s to about 90 s in the unlucky alignment, which stays inside what the
// wallboard's own stale indicator tolerates.
const PageStaleWhileRevalidate = 30 * time.Second

// FeedMaxAge is the Atom feed's budget, unchanged from what it already sent: a
// feed reader polls on its own schedule and a status-update timeline moves far
// more slowly than a rollup.
const FeedMaxAge = 5 * time.Minute

// Control returns the Cache-Control value for a public status-page response
// serving a page of this visibility.
//
// A world-readable answer carries stale-while-revalidate (see
// PageStaleWhileRevalidate); a gated one carries Gated and therefore no grace
// window at all — there is nothing to serve stale from a cache that was told
// not to store the body in the first place.
func Control(visibility string, maxAge time.Duration) string {
	if visibility != models.StatusPageVisibilityPublic {
		return Gated
	}

	return "public, max-age=" + strconv.Itoa(int(maxAge.Seconds())) +
		", stale-while-revalidate=" + strconv.Itoa(int(PageStaleWhileRevalidate.Seconds()))
}

// Apply writes Cache-Control and Vary onto a public status-page response.
func Apply(header http.Header, visibility string, maxAge time.Duration) {
	if visibility != models.StatusPageVisibilityPublic {
		ApplyGated(header)

		return
	}

	header.Set("Cache-Control", Control(visibility, maxAge))
	// Add, not Set: the process-wide compression wrapper (server.compression)
	// sits OUTSIDE every handler and adds its own "Vary: Accept-Encoding"
	// before this handler ever runs. Set would silently wipe that entry out,
	// leaving a compressed response that a CDN could key incorrectly on. See
	// the package doc's "Accept-Encoding is not this package's Vary" note.
	header.Add("Vary", VaryPublic)
}

// ApplyGated writes the never-shared directive. Used where no page is in hand
// — the 404/401 answers — so a shared cache cannot turn the public surface's
// error replies into a map of which pages exist.
func ApplyGated(header http.Header) {
	header.Set("Cache-Control", Gated)
	// Add, not Set — see the comment in Apply above.
	header.Add("Vary", VaryGated)
}
