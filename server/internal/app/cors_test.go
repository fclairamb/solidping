package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspageassets"
)

// okHandler is a minimal terminal handler for driving corsMiddleware directly
// in these tests, without a real route or a full router.
func okHandler(w http.ResponseWriter, _ *http.Request) error {
	w.WriteHeader(http.StatusOK)

	return nil
}

// TestIsPublicCORSPath pins which request paths are treated as genuinely
// public, credential-free surfaces (spec 2026-08-30-09) — the status pages
// API, their assets, the embed widget, and the PostHog ingest proxy — versus
// everything else, including a path that merely SHARES a prefix with one of
// them (/api/v1/orgs/... is both the public badge route's ancestor prefix and
// the prefix of every authenticated org-scoped route).
func TestIsPublicCORSPath(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(isPublicCORSPath(config.PostHogProxyPath+"/decide/"), "ingest proxy")
	r.True(isPublicCORSPath("/embed/v1/widget.js"), "embed widget")
	r.True(isPublicCORSPath("/api/v1/status-pages/acme/main/summary"), "public status page summary")
	r.True(isPublicCORSPath("/api/v1/status-pages/acme/main/badge"), "public status page badge")
	r.True(isPublicCORSPath(statuspageassets.PublicPathPrefix+"some-file-uid"), "public status page asset")

	r.False(isPublicCORSPath("/api/v1/orgs/acme/checks"), "authenticated org route")
	r.False(isPublicCORSPath("/api/v1/orgs/acme/status-pages"), "authenticated status-page management route")
	r.False(isPublicCORSPath("/api/v1/auth/login"), "auth route")
	r.False(isPublicCORSPath("/dash0/"), "dashboard SPA")
}

// TestCORSMiddleware_AllowlistedOriginGetsEchoedCredentialedResponse proves an
// origin that matches Config.ResolvedCORSAllowedOrigins gets its own Origin
// echoed back — exactly once — with Allow-Credentials and Vary: Origin.
func TestCORSMiddleware_AllowlistedOriginGetsEchoedCredentialedResponse(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &config.Config{}
	cfg.Server.CORSAllowedOrigins = []string{"https://dash.example.com"}
	server := &Server{config: cfg}

	handler := server.corsMiddleware(okHandler)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orgs/acme/checks", nil)
	req.Header.Set("Origin", "https://dash.example.com")

	r.NoError(handler(recorder, req))
	r.Equal(http.StatusOK, recorder.Code)
	r.Len(recorder.Header().Values("Access-Control-Allow-Origin"), 1)
	r.Equal("https://dash.example.com", recorder.Header().Get("Access-Control-Allow-Origin"))
	r.Equal("true", recorder.Header().Get("Access-Control-Allow-Credentials"))
	r.Contains(recorder.Header().Values("Vary"), "Origin")
}

// TestCORSMiddleware_NonAllowlistedOriginGetsNoAllowOriginHeader proves an
// origin NOT on the allowlist gets no Access-Control-Allow-Origin at all on a
// protected path — deny by default, same as any other unlisted cross-origin
// caller — rather than falling back to the old wildcard.
func TestCORSMiddleware_NonAllowlistedOriginGetsNoAllowOriginHeader(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &config.Config{}
	cfg.Server.CORSAllowedOrigins = []string{"https://dash.example.com"}
	server := &Server{config: cfg}

	handler := server.corsMiddleware(okHandler)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orgs/acme/checks", nil)
	req.Header.Set("Origin", "https://attacker.example.com")

	r.NoError(handler(recorder, req))
	r.Equal(http.StatusOK, recorder.Code)
	r.Empty(recorder.Header().Values("Access-Control-Allow-Origin"))
	r.Empty(recorder.Header().Get("Access-Control-Allow-Credentials"))
}

// TestCORSMiddleware_NoOriginHeaderIsUntouched proves a same-origin (or
// non-browser) request carrying no Origin header at all gets neither
// Allow-Origin nor Allow-Credentials nor a Vary: Origin it never needed.
func TestCORSMiddleware_NoOriginHeaderIsUntouched(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := &Server{config: &config.Config{}}
	handler := server.corsMiddleware(okHandler)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orgs/acme/checks", nil)

	r.NoError(handler(recorder, req))
	r.Empty(recorder.Header().Values("Access-Control-Allow-Origin"))
	r.Empty(recorder.Header().Values("Vary"))
}

// TestCORSMiddleware_PublicStatusPagePathAlwaysWildcardNoCredentials proves a
// genuinely public surface answers "*" with no credentials for ANY origin —
// including one nowhere near the operator's allowlist, exactly like a
// customer's own domain embedding a status page — and never both "*" and
// Allow-Credentials together.
func TestCORSMiddleware_PublicStatusPagePathAlwaysWildcardNoCredentials(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &config.Config{}
	// Deliberately does NOT include the caller's origin: a public surface
	// must work regardless of what is or isn't on the allowlist.
	cfg.Server.CORSAllowedOrigins = []string{"https://dash.example.com"}
	server := &Server{config: cfg}

	handler := server.corsMiddleware(okHandler)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, "/api/v1/status-pages/acme/main/summary", nil)
	req.Header.Set("Origin", "https://customers-own-site.example")

	r.NoError(handler(recorder, req))
	r.Len(recorder.Header().Values("Access-Control-Allow-Origin"), 1)
	r.Equal("*", recorder.Header().Get("Access-Control-Allow-Origin"))
	r.Empty(recorder.Header().Get("Access-Control-Allow-Credentials"))
}

// TestCORSMiddleware_OPTIONSShortCircuitsWithSameHeadersAsAGetWould proves the
// preflight branch decides headers the same way the actual-request branch
// does — a preflight to a public path still gets the wildcard, one to an
// allowlisted origin still gets the credentialed echo — rather than the
// short-circuit racing ahead of that decision.
func TestCORSMiddleware_OPTIONSShortCircuitsWithSameHeadersAsAGetWould(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &config.Config{}
	cfg.Server.CORSAllowedOrigins = []string{"https://dash.example.com"}
	server := &Server{config: cfg}

	handler := server.corsMiddleware(func(http.ResponseWriter, *http.Request) error {
		panic("OPTIONS must never reach the inner handler")
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/api/v1/orgs/acme/checks", nil)
	req.Header.Set("Origin", "https://dash.example.com")

	r.NoError(handler(recorder, req))
	r.Equal(http.StatusOK, recorder.Code)
	r.Equal("https://dash.example.com", recorder.Header().Get("Access-Control-Allow-Origin"))
	r.Equal("true", recorder.Header().Get("Access-Control-Allow-Credentials"))
}
