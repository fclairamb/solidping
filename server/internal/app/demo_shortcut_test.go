package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

// TestServeDemoShortcutEnabled covers the one-word /demo entry point (spec
// 2026-09-08-02): with demo.enabled on, GET /demo and /demo/ answer 302 (never
// 301 — browsers cache permanent redirects, and the shortcut must stop
// working the instant an operator turns the demo off) with the exact
// canonical Location, and never forward the incoming query string.
func TestServeDemoShortcutEnabled(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := &Server{config: &config.Config{Demo: config.DemoConfig{Enabled: true}}}

	for _, path := range []string{"/demo", "/demo/", "/demo?returnTo=/orgs/acme"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody)
		w := httptest.NewRecorder()
		r.NoError(s.serveDemoShortcut(w, req))

		resp := w.Result()
		defer func() { _ = resp.Body.Close() }()

		r.Equal(http.StatusFound, resp.StatusCode, "path %s", path)
		r.Equal("/d/login?demo=true", resp.Header.Get("Location"), "path %s", path)
	}
}

// TestServeDemoShortcutDisabled is the positive control the spec asks for: a
// future regression that starts redirecting /demo unconditionally (ignoring
// Demo.Enabled) must fail this test. With the demo off, /demo must behave
// EXACTLY like serveAppRoot does for any other unmatched path today — not a
// hardcoded status, but the same status serveAppRoot itself returns for the
// identical request, so the two can never silently drift apart.
func TestServeDemoShortcutDisabled(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := &Server{config: &config.Config{Demo: config.DemoConfig{Enabled: false}}}

	for _, path := range []string{"/demo", "/demo/"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody)
		gotW := httptest.NewRecorder()
		r.NoError(s.serveDemoShortcut(gotW, req))

		wantReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody)
		wantW := httptest.NewRecorder()
		r.NoError(s.serveAppRoot(wantW, wantReq))

		r.Equal(wantW.Result().StatusCode, gotW.Result().StatusCode, "path %s", path)
		r.Empty(gotW.Header().Get("Location"), "disabled demo must not redirect for %s", path)
	}
}

// TestServeDemoShortcutCustomHost404s covers isCustomHostForbidden: /demo on a
// resolved custom status-page domain must 404, never redirect a visitor into
// the SolidPing dashboard, exactly like /d is already forbidden there.
func TestServeDemoShortcutCustomHost404s(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := newCustomHostTestServer(t)
	server.config.Demo.Enabled = true

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := server.handlerWithCustomDomains(next)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/demo", http.NoBody)
	req.Host = "status.acme.com"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	r.Equal(http.StatusNotFound, w.Result().StatusCode)
}

// TestServeDemoCampaignShortcut covers /demo/<source>: a campaign link bounces
// to the canonical /demo/?utm_source=<source> with a 302, and the source
// segment is url-encoded on the way out so a hostile segment cannot escape the
// fixed /demo/ prefix (open redirect) or break the header (CRLF).
func TestServeDemoCampaignShortcut(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := &Server{config: &config.Config{Demo: config.DemoConfig{Enabled: true}}}

	cases := []struct {
		path string
		want string
	}{
		{path: "/demo/hackernews", want: "/demo/?utm_source=hackernews"},
		{path: "/demo/hackernews/", want: "/demo/?utm_source=hackernews"},
		{path: "/demo/print-flyer-2026", want: "/demo/?utm_source=print-flyer-2026"},
		// An incoming query is dropped, not merged — same rule as the plain
		// shortcut.
		{path: "/demo/hn?returnTo=/orgs/acme", want: "/demo/?utm_source=hn"},
		// Hostile segments stay inside the /demo/ prefix once encoded.
		{path: "/demo/a%20b", want: "/demo/?utm_source=a+b"},
		{path: "/demo/x%26utm_medium%3Devil", want: "/demo/?utm_source=x%26utm_medium%3Devil"},
		{path: "/demo/a/b", want: "/demo/?utm_source=a%2Fb"},
	}

	for _, tc := range cases {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, http.NoBody)
		w := httptest.NewRecorder()
		r.NoError(s.serveDemoCampaignShortcut(w, req))

		resp := w.Result()
		defer func() { _ = resp.Body.Close() }()

		r.Equal(http.StatusFound, resp.StatusCode, "path %s", tc.path)
		r.Equal(tc.want, resp.Header.Get("Location"), "path %s", tc.path)
	}
}

// TestServeDemoCampaignShortcutDisabled is the negative control: with demo
// mode off a campaign link must not advertise a demo that does not exist. It
// delegates to serveAppRoot, which 404s an unmatched path — it must never
// redirect.
func TestServeDemoCampaignShortcutDisabled(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := &Server{config: &config.Config{Demo: config.DemoConfig{Enabled: false}}}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/demo/hackernews", http.NoBody)
	w := httptest.NewRecorder()
	r.NoError(s.serveDemoCampaignShortcut(w, req))

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusNotFound, resp.StatusCode)
	r.Empty(resp.Header.Get("Location"))
}
