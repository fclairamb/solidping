package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

// TestMetricsHandlerNoTokenReturns404 pins the unset-token convention: /metrics
// must answer exactly like a route that was never registered (404), not a 401
// that would confirm the endpoint exists and merely needs a credential.
func TestMetricsHandlerNoTokenReturns404(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := &Server{config: &config.Config{}}
	handler := srv.metricsHandler()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", http.NoBody)
	w := httptest.NewRecorder()
	r.NoError(handler(w, req))
	r.Equal(http.StatusNotFound, w.Code)

	// Presenting a (wrong) bearer must not change the answer: disabled means
	// disabled, regardless of what the caller sends.
	req2 := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", http.NoBody)
	req2.Header.Set("Authorization", "Bearer whatever")
	w2 := httptest.NewRecorder()
	r.NoError(handler(w2, req2))
	r.Equal(http.StatusNotFound, w2.Code)
}

// TestMetricsHandlerWrongOrMissingBearerReturns401 covers the token-set case:
// no Authorization header, a non-bearer scheme, and a wrong token must all
// answer 401 with the generic {title, code} body — never a body that lets a
// caller distinguish "malformed" from "wrong value".
func TestMetricsHandlerWrongOrMissingBearerReturns401(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.Prometheus.ScrapeToken = "the-real-token"
	srv := &Server{config: cfg}
	handler := srv.metricsHandler()

	cases := []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"wrong token", "Bearer not-the-token"},
		{"basic auth instead of bearer", "Basic dXNlcjpwYXNz"},
		{"bearer with empty value", "Bearer "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rr := require.New(t)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", http.NoBody)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}

			w := httptest.NewRecorder()
			rr.NoError(handler(w, req))
			rr.Equal(http.StatusUnauthorized, w.Code)
			rr.Equal("application/json", w.Header().Get("Content-Type"))
			rr.Contains(w.Body.String(), `"code":"UNAUTHORIZED"`)
		})
	}
}

// TestMetricsHandlerCorrectBearerReturns200 proves the happy path: the right
// bearer token gets through to the real promhttp exposition (text/plain).
func TestMetricsHandlerCorrectBearerReturns200(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &config.Config{}
	cfg.Prometheus.ScrapeToken = "the-real-token"
	srv := &Server{config: cfg}
	handler := srv.metricsHandler()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", http.NoBody)
	req.Header.Set("Authorization", "Bearer the-real-token")
	w := httptest.NewRecorder()
	r.NoError(handler(w, req))
	r.Equal(http.StatusOK, w.Code)
	r.Contains(w.Header().Get("Content-Type"), "text/plain")
}

// TestMetricsBearerMatchesIsCaseInsensitiveScheme proves the "Bearer" scheme
// keyword is matched case-insensitively (RFC 7235 §2.1), like every other
// bearer check in this codebase.
func TestMetricsBearerMatchesIsCaseInsensitiveScheme(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.True(metricsBearerMatches("bearer tok", "tok"))
	r.True(metricsBearerMatches("BEARER tok", "tok"))
	r.True(metricsBearerMatches("Bearer tok", "tok"))
	r.False(metricsBearerMatches("Bearer wrong", "tok"))
	r.False(metricsBearerMatches("", "tok"))
	r.False(metricsBearerMatches("tok", "tok"), "the scheme prefix is required")
}

// TestLogMetricsScrapeTokenState pins the three boot-time states: disabled
// logs nothing here (SetupRoutes not registering the route already says so),
// enabled-without-token logs the "disabled, set the parameter" INFO line, and
// enabled-with-token logs the "requires a bearer" INFO line.
func TestLogMetricsScrapeTokenState(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	buf := captureLogs(t)
	cfg := &config.Config{}
	cfg.Prometheus.Enabled = false
	logMetricsScrapeTokenState(ctx, cfg)
	r.Empty(buf.String(), "disabled must not log anything from this function")

	buf = captureLogs(t)
	cfg.Prometheus.Enabled = true
	cfg.Prometheus.ScrapeToken = ""
	logMetricsScrapeTokenState(ctx, cfg)
	r.Contains(buf.String(), "metrics endpoint disabled, set metrics.scrape_token to enable scraping")

	buf = captureLogs(t)
	cfg.Prometheus.ScrapeToken = "set"
	logMetricsScrapeTokenState(ctx, cfg)
	r.Contains(buf.String(), "metrics endpoint requires a bearer scrape token")
}
