package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
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
