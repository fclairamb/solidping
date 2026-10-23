package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// metricsText renders a /metrics body; scale grows every counter and
// histogram so that the second scrape shows a positive delta.
func metricsText(scale int) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# TYPE solidping_check_executions_total counter\nsolidping_check_executions_total{status=\"up\"} %d\n", 10*scale)
	fmt.Fprintf(&b, "# TYPE solidping_checks_rate_limited_total counter\nsolidping_checks_rate_limited_total %d\n", scale)
	fmt.Fprintf(&b, "# TYPE solidping_db_busy_retries_total counter\nsolidping_db_busy_retries_total %d\n", 2*scale)

	hist := func(name, label, value string) {
		fmt.Fprintf(&b, "# TYPE %s histogram\n", name)
		fmt.Fprintf(&b, "%s_bucket{%s=%q,le=\"0.001\"} %d\n", name, label, value, 5*scale)
		fmt.Fprintf(&b, "%s_bucket{%s=%q,le=\"0.1\"} %d\n", name, label, value, 9*scale)
		fmt.Fprintf(&b, "%s_bucket{%s=%q,le=\"+Inf\"} %d\n", name, label, value, 10*scale)
		fmt.Fprintf(&b, "%s_sum{%s=%q} %d\n%s_count{%s=%q} %d\n", name, label, value, scale, name, label, value, 10*scale)
	}

	hist("solidping_check_stage_duration_seconds", "stage", "execute")
	hist("solidping_db_query_duration_seconds", "backend", "sqlite")
	hist("solidping_http_request_duration_seconds", "route", "/api/v1/orgs/:org/checks")

	for _, g := range []string{"max_open", "open", "in_use", "idle"} {
		fmt.Fprintf(&b, "# TYPE solidping_db_pool_%s_connections gauge\nsolidping_db_pool_%s_connections{backend=\"sqlite\"} 4\n", g, g)
	}

	fmt.Fprintf(&b, "# TYPE solidping_db_pool_wait_count_total counter\nsolidping_db_pool_wait_count_total{backend=\"sqlite\"} %d\n", scale)
	fmt.Fprintf(&b, "# TYPE solidping_claim_jobs_result_total counter\nsolidping_claim_jobs_result_total{outcome=\"jobs\"} %d\n", 3*scale)

	return b.String()
}

type fakeServer struct {
	scrapes   atomic.Int32
	deleted   atomic.Int32
	created   atomic.Int32
	loginCode int
	bulkCode  int
	metrics   int
}

func (f *fakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/mgmt/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		if f.loginCode != 0 {
			w.WriteHeader(f.loginCode)
			_, _ = w.Write([]byte(`{"accessToken":""}`))

			return
		}

		_, _ = w.Write([]byte(`{"accessToken":"tok"}`))
	})
	mux.HandleFunc("/api/v1/test/checks/all", func(w http.ResponseWriter, _ *http.Request) {
		f.deleted.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v1/test/checks/bulk", func(w http.ResponseWriter, _ *http.Request) {
		f.created.Add(1)

		if f.bulkCode != 0 {
			w.WriteHeader(f.bulkCode)

			return
		}
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if f.metrics != 0 {
			w.WriteHeader(f.metrics)

			return
		}

		if r.Header.Get("Authorization") != "Bearer scrape" {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		_, _ = w.Write([]byte(metricsText(int(f.scrapes.Add(1)))))
	})

	return mux
}

func TestRunWritesReport(t *testing.T) {
	f := &fakeServer{}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)

	dir := t.TempDir()

	err := run(runOpts{
		apiURL: srv.URL, backend: "sqlite", nChecks: 3, duration: 50 * time.Millisecond,
		period: "10s", outputDir: dir, scrapeToken: "scrape",
	})
	require.NoError(t, err)

	files, err := filepath.Glob(filepath.Join(dir, "*-sqlite.md"))
	require.NoError(t, err)
	require.Len(t, files, 1)

	raw, err := os.ReadFile(files[0])
	require.NoError(t, err)

	report := string(raw)
	assert.Contains(t, report, "| Total check executions | 10 |")
	assert.Contains(t, report, "| execute | 10 |")
	assert.Contains(t, report, "| sqlite | 10 |")
	assert.Contains(t, report, "| `/api/v1/orgs/:org/checks` | 10 |")
	assert.Contains(t, report, "| jobs | 3 |")
	assert.Equal(t, int32(2), f.deleted.Load(), "pre-clean and post-clean")
	assert.Equal(t, int32(1), f.created.Load())
}

func TestRunKeepChecksSkipsPostClean(t *testing.T) {
	f := &fakeServer{}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)

	require.NoError(t, run(runOpts{
		apiURL: srv.URL, backend: "x", nChecks: 1, duration: time.Millisecond,
		outputDir: t.TempDir(), scrapeToken: "scrape", keep: true,
	}))
	assert.Equal(t, int32(1), f.deleted.Load())
}

func TestRunErrors(t *testing.T) {
	tests := []struct {
		name    string
		fake    *fakeServer
		token   string
		wantErr string
	}{
		{"login rejected", &fakeServer{loginCode: http.StatusUnauthorized}, "scrape", "login"},
		{"bulk create rejected", &fakeServer{bulkCode: http.StatusInternalServerError}, "scrape", "bulk create"},
		{"metrics need a token", &fakeServer{}, "", "baseline scrape"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.fake.handler())
			t.Cleanup(srv.Close)

			err := run(runOpts{
				apiURL: srv.URL, nChecks: 1, duration: time.Millisecond,
				outputDir: t.TempDir(), scrapeToken: tt.token,
			})
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestHelpersErrorPaths(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	_, err := login(ctx, "http://127.0.0.1:1")
	require.Error(t, err)
	require.Error(t, deleteAllChecks(ctx, "http://127.0.0.1:1", "t"))
	require.Error(t, bulkCreateChecks(ctx, "http://127.0.0.1:1", "t", 1, "1s", "http://x"))

	_, err = scrapeMetrics(ctx, "http://127.0.0.1:1", "")
	require.Error(t, err)

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, waitForHealth(canceled, "http://127.0.0.1:1"), context.Canceled)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_, _ = w.Write([]byte(`{`))
		case "/api/v1/test/checks/all":
			w.WriteHeader(http.StatusForbidden)
		case "/metrics":
			_, _ = w.Write([]byte("this is not { prometheus"))
		}
	}))
	t.Cleanup(srv.Close)

	_, err = login(ctx, srv.URL)
	require.Error(t, err)
	require.ErrorContains(t, deleteAllChecks(ctx, srv.URL, "t"), "403")

	_, err = scrapeMetrics(ctx, srv.URL, "")
	require.Error(t, err)
}

func TestStartTargetServer(t *testing.T) {
	t.Parallel()

	srv := startTargetServer(time.Millisecond)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL) //nolint:noctx
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestFmtSec(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "—", fmtSec(0))
	assert.Equal(t, "500µs", fmtSec(0.0005))
	assert.Equal(t, "12.5ms", fmtSec(0.0125))
	assert.Equal(t, "2.50s", fmtSec(2.5))
}

func snapshotOf(t *testing.T, scale int) *metricsSnapshot {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(metricsText(scale)))
	}))
	t.Cleanup(srv.Close)

	snap, err := scrapeMetrics(t.Context(), srv.URL, "")
	require.NoError(t, err)

	return snap
}

func TestMetricHelpers(t *testing.T) {
	t.Parallel()

	base, final := snapshotOf(t, 1), snapshotOf(t, 3)

	assert.InDelta(t, 20, counterDelta(base, final, "solidping_check_executions_total"), 0)
	assert.InDelta(t, 0, counterDelta(base, final, "missing"), 0)
	assert.InDelta(t, 4, poolGaugeByBackend(final, "solidping_db_pool_open_connections", "sqlite"), 0)
	assert.InDelta(t, 0, poolGaugeByBackend(final, "solidping_db_pool_open_connections", "postgres"), 0)
	assert.InDelta(t, 0, poolGaugeByBackend(final, "missing", "sqlite"), 0)
	assert.InDelta(t, 2, poolCounterByBackend(base, final, "solidping_db_pool_wait_count_total", "sqlite"), 0)
	assert.InDelta(t, 4, readBackendCounter(final, "solidping_db_pool_open_connections", "sqlite"), 0, "gauges are read too")
	assert.InDelta(t, 0, readBackendCounter(final, "solidping_db_pool_wait_count_total", "postgres"), 0)
	assert.InDelta(t, 0, readBackendCounter(final, "missing", "sqlite"), 0)
	assert.InDelta(t, 6, outcomeCounter(base, final, "solidping_claim_jobs_result_total", "jobs"), 0)
	assert.InDelta(t, 0, outcomeCounter(base, final, "solidping_claim_jobs_result_total", "empty"), 0)
	assert.InDelta(t, 0, outcomeCounter(base, final, "missing", "jobs"), 0)

	assert.InDelta(t, 20, stageHistogramCount(base, final, "execute"), 0)
	assert.InDelta(t, 20, dbQueryCountByBackend(base, final, "sqlite"), 0)
	assert.InDelta(t, 20, routeHistogramCount(base, final, "/api/v1/orgs/:org/checks"), 0)
	assert.InDelta(t, 0, stageHistogramCount(base, final, "fetch"), 0)

	// 10 of 20 samples fall in the first bucket (le=0.001), so p50 sits at its upper edge.
	assert.InDelta(t, 0.001, stageHistogramQuantile(base, final, "execute", 0.5), 1e-9)
	// p95 lies in the (0.001, 0.1] bucket, between its bounds.
	p95 := dbQueryQuantileByBackend(base, final, "sqlite", 0.85)
	assert.Greater(t, p95, 0.001)
	assert.Less(t, p95, 0.1)
	// p99 falls in the +Inf bucket and is capped at the last finite bound.
	assert.InDelta(t, 0.1, routeHistogramQuantile(base, final, "/api/v1/orgs/:org/checks", 0.99), 1e-9)
	// Unknown family or label, and an empty delta, give zero.
	assert.InDelta(t, 0, stageHistogramQuantile(base, final, "fetch", 0.5), 0)
	assert.InDelta(t, 0, labelHistogramQuantile(base, final, "missing", "a", "b", 0.5), 0)
	assert.InDelta(t, 0, stageHistogramQuantile(final, final, "execute", 0.5), 0)

	// A baseline without the family is treated as empty.
	empty := &metricsSnapshot{families: nil}
	assert.InDelta(t, 0.001, stageHistogramQuantile(empty, base, "execute", 0.5), 1e-9)
}

func TestBuildReportWithoutLoad(t *testing.T) {
	t.Parallel()

	snap := &metricsSnapshot{}
	report := buildReport(runOpts{backend: "postgres", nChecks: 2, period: "5s"}, 0, snap, snap)

	assert.Contains(t, report, "Backend: `postgres`")
	assert.Contains(t, report, "| Achieved checks/min | **0.0** |")
	assert.NotContains(t, report, "| execute |")
}
