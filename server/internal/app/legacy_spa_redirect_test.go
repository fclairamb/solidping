package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

// TestLegacySPARedirectPreservesPathAndQuery locks in the permanent redirects
// off the retired /dash0 and /status0 prefixes (spec 2026-09-09-01 §3).
//
// These are the links that already escaped into the world — emails sent last
// month, bookmarks, chat messages, the marketing site, search-engine indexes —
// so the contract is exact: 301 (permanent, no sunset), the remaining path
// carried over untouched, and the query string preserved VERBATIM. A dropped
// ?demo=true or ?returnTo= is a silently broken flow, not a cosmetic bug.
func TestLegacySPARedirectPreservesPathAndQuery(t *testing.T) {
	t.Parallel()

	server := &Server{config: &config.Config{}}

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "dash0 root", path: "/dash0", want: "/d"},
		{name: "dash0 root slash", path: "/dash0/", want: "/d/"},
		{name: "dash0 deep path", path: "/dash0/orgs/acme/checks", want: "/d/orgs/acme/checks"},
		{
			name: "dash0 query preserved verbatim",
			path: "/dash0/login?demo=true",
			want: "/d/login?demo=true",
		},
		{
			name: "dash0 returnTo keeps its percent-encoding",
			path: "/dash0/orgs/acme/login?returnTo=%2Fdash0%2Forgs%2Facme%2Fchecks%3Fq%3D1",
			want: "/d/orgs/acme/login?returnTo=%2Fdash0%2Forgs%2Facme%2Fchecks%3Fq%3D1",
		},
		{
			name: "dash0 path segment that repeats the prefix is not rewritten twice",
			path: "/dash0/orgs/dash0/checks",
			want: "/d/orgs/dash0/checks",
		},
		{name: "status0 root", path: "/status0", want: "/s"},
		{name: "status0 page", path: "/status0/acme/public", want: "/s/acme/public"},
		{
			name: "status0 query preserved",
			path: "/status0/acme/public?preview=1",
			want: "/s/acme/public?preview=1",
		},
		{name: "legacy asset path", path: "/dash0/assets/index-abc.js", want: "/d/assets/index-abc.js"},
		// The service-worker script: a browser refuses a worker script that
		// answers with a redirect, which is exactly WHY the old registration
		// has to be unregistered by hand (web/dash0/src/lib/service-worker.ts).
		{name: "legacy service worker script", path: "/dash0/sw.js", want: "/d/sw.js"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, testCase.path, http.NoBody)
			w := httptest.NewRecorder()

			r.NoError(server.serveLegacySPARedirect(w, req))

			resp := w.Result()
			defer func() { _ = resp.Body.Close() }()

			r.Equal(http.StatusMovedPermanently, resp.StatusCode)
			r.Equal(testCase.want, resp.Header.Get("Location"))
		})
	}
}

// TestLegacySPALocationLeavesEverythingElseAlone is the negative control: only
// the two retired prefixes are rewritten, and only as a whole leading segment.
// A path that merely STARTS with the same letters must not be captured.
func TestLegacySPALocationLeavesEverythingElseAlone(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, path := range []string{
		"/", "/d", "/d/orgs/acme", "/s", "/s/acme",
		"/dash0x", "/dash0x/orgs", "/status0x",
		"/docs", "/demo", "/api/v1/orgs/acme/checks", "/metrics",
	} {
		_, ok := legacySPALocation(path)
		r.False(ok, "path %s must not be treated as a legacy SPA path", path)
	}
}

// TestServeAppRootRedirectsRootToDashboard covers the "/" -> dashboard hop. It
// is a 302, not a 301: the destination is a product decision that has already
// moved once, and a permanent redirect is cached by browsers essentially
// forever.
func TestServeAppRootRedirectsRootToDashboard(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := &Server{config: &config.Config{}}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
	w := httptest.NewRecorder()

	r.NoError(server.serveAppRoot(w, req))

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusFound, resp.StatusCode)
	r.Equal("/d/", resp.Header.Get("Location"))
}

// TestServeAppRootUnmatchedPathIs404 pins the behavior change from retiring
// the legacy web/dash app (spec 2026-09-09-01 §1): an unmatched path used to
// render the OLD dashboard shell with a 200. It is now a plain HTML 404 — no
// SPA shell, and in particular no 200 that would let a typo'd URL look like a
// working page to a crawler.
func TestServeAppRootUnmatchedPathIs404(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := &Server{config: &config.Config{}}

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/some/marketing/page", http.NoBody)
	w := httptest.NewRecorder()

	r.NoError(server.serveAppRoot(w, req))

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusNotFound, resp.StatusCode)
	r.Contains(resp.Header.Get("Content-Type"), "text/html")
	r.Contains(w.Body.String(), "<!doctype html")
	// Not the SPA shell: nothing that would boot an application.
	r.NotContains(w.Body.String(), `id="root"`)
}

// TestRedirectRuleMatchesWholeSegments guards the dev-proxy rule matcher
// (SP_REDIRECTS) against the same single-letter-prefix trap as the custom-host
// deny-list. `make dev` now ships "/d:localhost:5174/d,/s:localhost:5175/s";
// with a raw strings.HasPrefix that would proxy /docs, /demo and /dashboard
// into the dashboard's Vite server, and /s would swallow every path that
// happens to start with an s.
func TestRedirectRuleMatchesWholeSegments(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(redirectRuleMatches("/d", "/d"))
	r.True(redirectRuleMatches("/d/", "/d"))
	r.True(redirectRuleMatches("/d/orgs/acme", "/d"))
	r.True(redirectRuleMatches("/s/acme/main", "/s"))

	r.False(redirectRuleMatches("/docs", "/d"))
	r.False(redirectRuleMatches("/docs/intro", "/d"))
	r.False(redirectRuleMatches("/demo", "/d"))
	r.False(redirectRuleMatches("/dashboard", "/d"))
	r.False(redirectRuleMatches("/status", "/s"))
	r.False(redirectRuleMatches("/some/path", "/s"))

	// The documented catch-all rule still matches everything.
	r.True(redirectRuleMatches("/anything/at/all", "/"))

	// A trailing slash in the configured prefix is not a second segment.
	r.True(redirectRuleMatches("/dash0/orgs", "/dash0/"))
	r.False(redirectRuleMatches("/dash0x", "/dash0/"))
}
