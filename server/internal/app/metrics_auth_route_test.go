package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

// metricsRouteServer boots a real server (real NewServer + real
// InitializeSystemConfig + real SetupRoutes, in the same order main.go uses)
// over an in-memory SQLite DB, optionally pre-seeding the metrics.scrape_token
// system parameter BEFORE InitializeSystemConfig runs — the only way a
// database-only token (no env var) can reach cfg. Mirrors versionRouteServer.
//
// enabled=true is exercised by exactly one test in this package
// (TestMetricsRouteDBTokenTakesEffectAndLabelsIntact): SetupRoutes calls
// prommetrics.Register(prometheus.DefaultRegisterer) when Prometheus.Enabled,
// and MustRegister panics on a second registration of the same collectors —
// so a second enabled=true boot anywhere else in this package would crash the
// whole test binary.
func metricsRouteServer(t *testing.T, enabled bool, dbToken string) (*httptest.Server, string) {
	t.Helper()

	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "metrics-route-secret"
	cfg.Prometheus.Enabled = enabled

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))

	if dbToken != "" {
		r.NoError(server.dbService.SetSystemParameter(ctx, string(systemconfig.KeyMetricsScrapeToken), dbToken, true))
	}

	buf := captureLogs(t)
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	logs := buf.String()

	server.SetupRoutes(ctx)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	return ts, logs
}

// TestMetricsRouteDisabledAlwaysWins404 pins the master-switch rule: with
// Prometheus.Enabled=false the route is never registered, so /metrics 404s
// even when a scrape token is sitting in the database — SP_PROMETHEUS_ENABLED
// wins over metrics.scrape_token, never the other way round.
func TestMetricsRouteDisabledAlwaysWins404(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	ts, _ := metricsRouteServer(t, false, "a-token-that-must-be-ignored")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/metrics", http.NoBody)
	r.NoError(err)
	req.Header.Set("Authorization", "Bearer a-token-that-must-be-ignored")

	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusNotFound, resp.StatusCode)
}

// TestMetricsRouteDBTokenTakesEffectAndLabelsIntact is the single
// Prometheus.Enabled=true boot in this package (see metricsRouteServer's
// comment). It proves:
//   - a token stored ONLY in the database (no env var) reaches the handler
//     through InitializeSystemConfig's overlay — the failure mode the spec
//     calls out ("if the handler reads the token once at route-build time, a
//     DB-set token would be ignored");
//   - the boot log reflects the resolved state;
//   - an unauthenticated request is rejected once a token is configured;
//   - an authenticated scrape sees per-org labels intact
//     (CheckExecutions{organization=...}), per the spec's point 3.
func TestMetricsRouteDBTokenTakesEffectAndLabelsIntact(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const token = "db-only-scrape-token"

	ts, logs := metricsRouteServer(t, true, token)
	r.Contains(logs, "metrics endpoint requires a bearer scrape token")

	// Unauthenticated: 401, the token is configured.
	unauthReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/metrics", http.NoBody)
	r.NoError(err)

	resp, err := http.DefaultClient.Do(unauthReq)
	r.NoError(err)
	_ = resp.Body.Close()
	r.Equal(http.StatusUnauthorized, resp.StatusCode)

	// Give the scrape something org-labeled to see.
	prommetrics.RecordExecution("http", "up", "eu", "acme", 12.3)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/metrics", http.NoBody)
	r.NoError(err)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err = http.DefaultClient.Do(req)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)
	r.Contains(string(body), "solidping_check_executions_total")
	r.Contains(string(body), `organization="acme"`)
}
