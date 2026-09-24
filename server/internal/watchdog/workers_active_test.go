package watchdog_test

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/watchdog"
)

// workersActiveSeries gathers solidping_workers_active off a private registry
// and returns value per region label.
func workersActiveSeries(t *testing.T) map[string]float64 {
	t.Helper()

	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(prommetrics.WorkersActive))

	families, err := reg.Gather()
	require.NoError(t, err)

	out := make(map[string]float64)

	for _, family := range families {
		if family.GetName() != "solidping_workers_active" {
			continue
		}

		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "region" {
					out[label.GetValue()] = metric.GetGauge().GetValue()
				}
			}
		}
	}

	return out
}

// TestWatchdogPublishesWorkersActive wires solidping_workers_active from the
// region pass (spec 2026-09-25-01): one live cloud worker reads 1, the same
// worker gone stale reads 0, and no private (`@`) label is ever emitted even
// while a private region has a live agent.
//
// Not parallel: PublishMetrics resets and rewrites a process-global gauge, so
// a concurrent run would race these reads. A non-parallel top-level test runs
// before any parallel test in the package is released.
//
//nolint:paralleltest // mutates the process-global solidping_workers_active gauge
func TestWatchdogPublishesWorkersActive(t *testing.T) {
	r := require.New(t)
	env := newTestEnv(t)

	worker := env.registerLiveWorker(t, "w-eu-1", "eu-1")

	// A private region with a live agent and a check: it must be served (not
	// a ghost) and still never become a gauge label.
	env.createOrgCheck(t, env.org.Slug, []string{privateX})
	env.insertLiveOrgAgent(t, env.org.UID, privateX)

	report := env.watchdog.Evaluate(t.Context(), enabledConfig())
	r.Empty(report.Failed)
	r.Equal(1, report.CloudWorkersActive["eu-1"])
	watchdog.PublishMetrics(report)

	r.InDelta(1.0, testutil.ToFloat64(prommetrics.WorkersActive.WithLabelValues("eu-1")), 0.001)

	series := workersActiveSeries(t)
	r.Contains(series, "healthy", "declared cloud regions are exported, at 0 when unserved")
	r.InDelta(0.0, series["healthy"], 0.001)

	for region := range series {
		r.False(strings.HasPrefix(region, regions.PrivateRegionPrefix),
			"private region %q must never be a workers_active label", region)
	}

	// The worker goes stale: the next pass must read 0, not freeze at 1.
	stale := time.Now().Add(-regions.WorkerLivenessWindow - time.Minute)
	r.NoError(env.db.UpdateWorker(t.Context(), worker.UID, models.WorkerUpdate{LastActiveAt: &stale}))

	report = env.watchdog.Evaluate(t.Context(), enabledConfig())
	r.Empty(report.Failed)
	watchdog.PublishMetrics(report)

	r.InDelta(0.0, testutil.ToFloat64(prommetrics.WorkersActive.WithLabelValues("eu-1")), 0.001)

	// Reset() drops a region that vanished from the report: seed a bogus
	// series, publish again, and it must be gone.
	prommetrics.SetWorkersActive("vanished-region", 3)
	watchdog.PublishMetrics(report)

	series = workersActiveSeries(t)
	r.NotContains(series, "vanished-region")
	r.Equal(len(report.CloudWorkersActive), testutil.CollectAndCount(prommetrics.WorkersActive))
}

// TestWatchdogFailedRegionPassLeavesWorkersActiveAlone: a failed dark-region
// detector knows nothing, so the report carries no counts and publishing it
// must not reset the gauge to "0 workers everywhere".
//
//nolint:paralleltest // mutates the process-global solidping_workers_active gauge
func TestWatchdogFailedRegionPassLeavesWorkersActiveAlone(t *testing.T) {
	r := require.New(t)
	env := newTestEnv(t)

	broken := watchdog.NewService(env.db, failingRegionHealth{err: errFixture})
	broken.SetNow(func() time.Time { return pinnedNow })

	prommetrics.WorkersActive.Reset()
	prommetrics.SetWorkersActive("eu-1", 2)

	report := broken.Evaluate(t.Context(), enabledConfig())
	r.False(report.DetectorSucceeded(watchdog.DetectorDarkRegion))
	r.Nil(report.CloudWorkersActive)

	watchdog.PublishMetrics(report)

	r.InDelta(2.0, testutil.ToFloat64(prommetrics.WorkersActive.WithLabelValues("eu-1")), 0.001,
		"a failed region pass must leave the last known value in place")
}
