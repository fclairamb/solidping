package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// activePostHog builds a config whose PostHog is active and whose upstream is
// the given fake host.
func activePostHog(host string) *config.Config {
	return &config.Config{PostHog: config.PostHogConfig{
		Enabled:       true,
		ProjectAPIKey: "phc_public_key",
		Host:          host,
	}}
}

// TestProxyPostHogForwardsIngestion proves the first-party /ingest path reaches
// the upstream with the prefix stripped, the upstream's own Host header, and the
// visitor IP preserved for geolocation.
func TestProxyPostHogForwardsIngestion(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var gotPath, gotHost, gotForwardedFor, gotQuery string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.Path
		gotHost = req.Host
		gotForwardedFor = req.Header.Get("X-Forwarded-For")
		gotQuery = req.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	server := &Server{config: activePostHog(upstream.URL)}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, config.PostHogProxyPath+"/i/v0/e/?ver=1", nil)
	req.RemoteAddr = "203.0.113.7:54321"

	r.NoError(server.proxyPostHog(recorder, req))
	r.Equal(http.StatusOK, recorder.Code)
	r.Equal("/i/v0/e/", gotPath)
	r.Equal("ver=1", gotQuery)
	r.Contains(gotHost, "127.0.0.1", "the upstream must see its own host, not the SolidPing origin")
	r.Contains(gotForwardedFor, "203.0.113.7", "the visitor IP must reach PostHog for geolocation")
}

// TestProxyPostHog404sWhenAnalyticsOff proves the route is inert on a
// deployment that never configured PostHog.
func TestProxyPostHog404sWhenAnalyticsOff(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := &Server{config: &config.Config{PostHog: config.PostHogConfig{Enabled: true}}}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet, config.PostHogProxyPath+"/static/array.js", nil)

	r.NoError(server.proxyPostHog(recorder, req))
	r.Equal(http.StatusNotFound, recorder.Code)
}

// TestProxyPostHogStripsUpstreamCORSHeaders proves proxyPostHog's
// ModifyResponse removes every Access-Control-* header PostHog Cloud answers
// with. This is called directly, WITHOUT corsMiddleware in front of it (unlike
// production), so any Access-Control-* header still present on the recorder
// afterwards can only have come from the upstream response — proving it is
// ModifyResponse doing the stripping, not some accident of how corsMiddleware
// would have overwritten it anyway (spec 2026-08-30-09 proposal item 2).
func TestProxyPostHogStripsUpstreamCORSHeaders(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// PostHog Cloud answers its own, well-formed CORS headers.
		w.Header().Set("Access-Control-Allow-Origin", req.Header.Get("Origin"))
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	server := &Server{config: activePostHog(upstream.URL)}

	recorder := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, config.PostHogProxyPath+"/i/v0/e/?ver=1", nil)
	req.Header.Set("Origin", "https://www.solidping.io")

	r.NoError(server.proxyPostHog(recorder, req))
	r.Equal(http.StatusOK, recorder.Code)
	r.Empty(recorder.Header().Values("Access-Control-Allow-Origin"))
	r.Empty(recorder.Header().Values("Access-Control-Allow-Credentials"))
}

// registerIngestRoutes mirrors SetupRoutes' registration of the PostHog
// ingest proxy exactly (GET, POST, OPTIONS all on config.PostHogProxyPath),
// so the CORS pipeline tests below drive the real production wiring — the
// global middleware chain from buildMainGroup plus this route table — rather
// than a hand-assembled imitation of it.
func registerIngestRoutes(server *Server, mainGroup *httpx.Group) {
	mainGroup.GET(config.PostHogProxyPath+"/*path", server.proxyPostHog)
	mainGroup.POST(config.PostHogProxyPath+"/*path", server.proxyPostHog)
	mainGroup.OPTIONS(config.PostHogProxyPath+"/*path", func(_ http.ResponseWriter, _ *http.Request) error {
		return nil
	})
}

// TestIngestCORS_DuplicateUpstreamHeadersAreCollapsedToOne drives a real POST
// through the full production chain (buildMainGroup's corsMiddleware +
// proxyPostHog) against an upstream that answers its own well-formed CORS
// headers, exactly like PostHog Cloud does. The bug this spec fixes is
// DUPLICATION: reading a single header value would pass even when the
// response actually carries it twice, which is what a real browser rejects
// outright even though curl cannot tell the difference — so this asserts the
// COUNT, not just a value.
func TestIngestCORS_DuplicateUpstreamHeadersAreCollapsedToOne(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", req.Header.Get("Origin"))
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	server := &Server{config: activePostHog(upstream.URL)}

	router := httpx.New()
	mainGroup := server.buildMainGroup(t.Context(), router)
	registerIngestRoutes(server, mainGroup)

	ts := httptest.NewServer(router)
	defer ts.Close()

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodPost, ts.URL+config.PostHogProxyPath+"/i/v0/e/?ver=1", http.NoBody)
	r.NoError(err)
	req.Header.Set("Origin", "https://www.solidping.io")

	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)
	r.Len(resp.Header.Values("Access-Control-Allow-Origin"), 1)
	r.Equal("*", resp.Header.Get("Access-Control-Allow-Origin"))
	r.Empty(resp.Header.Values("Access-Control-Allow-Credentials"))
}

// TestIngestCORS_OPTIONSPreflightReturns200WithHeaders proves a preflighted
// request to /ingest (one carrying a custom header or a JSON content type)
// reaches corsMiddleware instead of the bare 405-with-no-CORS-headers the
// route table produced before OPTIONS was registered alongside GET/POST
// (spec 2026-08-30-09 proposal item 3).
func TestIngestCORS_OPTIONSPreflightReturns200WithHeaders(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Upstream is never dialed: corsMiddleware answers OPTIONS itself, before
	// proxyPostHog ever runs.
	server := &Server{config: activePostHog("http://127.0.0.1:1")}

	router := httpx.New()
	mainGroup := server.buildMainGroup(t.Context(), router)
	registerIngestRoutes(server, mainGroup)

	ts := httptest.NewServer(router)
	defer ts.Close()

	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodOptions, ts.URL+config.PostHogProxyPath+"/decide/?v=3", http.NoBody)
	r.NoError(err)
	req.Header.Set("Origin", "https://www.solidping.io")
	req.Header.Set("Access-Control-Request-Method", "POST")

	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	r.Equal(http.StatusOK, resp.StatusCode)
	r.Len(resp.Header.Values("Access-Control-Allow-Origin"), 1)
	r.Equal("*", resp.Header.Get("Access-Control-Allow-Origin"))
	r.NotEmpty(resp.Header.Get("Access-Control-Allow-Methods"))
}
