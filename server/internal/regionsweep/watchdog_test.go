package regionsweep_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/regionsweep"
	"github.com/fclairamb/solidping/server/internal/watchdog"
)

// watchdogConfig is the platform_watchdog as the env's operator() wrote it.
func (e *testEnv) watchdogConfig(t *testing.T) *watchdog.Config {
	t.Helper()

	cfg, err := watchdog.LoadConfig(t.Context(), e.db)
	require.NoError(t, err)

	return cfg
}

// runWatchdog runs one hourly watchdog pass the way the job does and returns
// the fingerprints its digest would announce.
func (e *testEnv) runWatchdog(t *testing.T) []string {
	t.Helper()

	cfg := e.watchdogConfig(t)
	svc := watchdog.NewService(e.db, e.checks)

	report := svc.Evaluate(t.Context(), cfg)
	transitions, err := svc.Reconcile(t.Context(), report.Filtered(cfg.Severity()), cfg)
	require.NoError(t, err)

	out := make([]string, 0, len(transitions))
	for _, transition := range watchdog.NotifiableTransitions(transitions) {
		out = append(out, fmt.Sprintf("%s:%s", transition.Kind, transition.Fingerprint))
	}

	return out
}

// strandWide strands enough overdue jobs in darkRegion to pass the hourly
// watchdog's own 5-job / 10-minute bar.
func (e *testEnv) strandWide(t *testing.T) {
	t.Helper()

	e.strand(t)

	for i := range 6 {
		e.checkScheduled(t, e.acme, fmt.Sprintf("Wide %d", i), time.Now().Add(-20*time.Minute), darkRegion)
	}
}

// TestSweepThenWatchdogDoesNotDoubleNotify: the sweep sees the region first
// and pages; the next hourly watchdog run reads the shared marker as ongoing
// and says nothing about it.
func TestSweepThenWatchdogDoesNotDoubleNotify(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.strandWide(t)
	env.operator(t, true)

	dark := transition(env.sweep(t), darkRegion)
	r.NotNil(dark)
	r.True(dark.OperatorNotified)

	fingerprint := watchdog.DarkRegionFingerprint(darkRegion)
	for _, notified := range env.runWatchdog(t) {
		r.NotContains(notified, fingerprint, "the digest must not re-announce the sweep's outage")
	}
}

// TestWatchdogThenSweepDoesNotDoubleNotify: the digest saw the region first
// (the sweep was not running yet); the sweep's transition then stays quiet
// for the operator — but still tells the orgs, which the digest never does.
func TestWatchdogThenSweepDoesNotDoubleNotify(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.strandWide(t)
	env.admin(t, env.acme, "alice@acme.com")
	env.operator(t, true)

	r.Contains(env.runWatchdog(t), "new:"+watchdog.DarkRegionFingerprint(darkRegion),
		"positive control: the digest reports the dark region")

	dark := transition(env.sweep(t), darkRegion)
	r.NotNil(dark)
	r.False(dark.OperatorNotified, "the operator was already told by the digest")
	r.Empty(env.emails(t, operatorEmail, "lauterbourg is dark"))
	r.Equal([]string{env.acme.UID}, dark.OrgsNotified)
}

// TestWatchdogDoesNotResolveASweepOwnedOutage: a dark region with one pinned
// check never passes the watchdog's 5-job bar. The hourly run must not read
// "no anomaly" as a recovery of the outage the sweep announced.
func TestWatchdogDoesNotResolveASweepOwnedOutage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.strand(t)
	env.check(t, env.acme, "Pinned API", darkRegion)
	env.operator(t, true)

	r.True(transition(env.sweep(t), darkRegion).OperatorNotified)

	fingerprint := watchdog.DarkRegionFingerprint(darkRegion)
	for _, notified := range env.runWatchdog(t) {
		r.NotContains(notified, fingerprint, "no resolved notice while the sweep still holds the region dark")
	}

	entry, err := env.db.GetStateEntry(t.Context(), nil, watchdog.StateKeyPrefix+fingerprint)
	r.NoError(err)
	r.NotNil(entry, "the shared marker survives the watchdog run")
}

// TestSweepWorksWithWatchdogDisabled: `platform_watchdog.enabled = false`
// only means there is nobody to page. The sweep still transitions, records
// state, notifies orgs — and never writes the shared marker for a notice that
// never went out.
func TestSweepWorksWithWatchdogDisabled(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	env.strand(t)
	env.check(t, env.acme, "Pinned API", darkRegion)
	env.admin(t, env.acme, "alice@acme.com")
	env.operator(t, false)

	dark := transition(env.sweep(t), darkRegion)
	r.NotNil(dark)
	r.False(dark.OperatorNotified)
	r.True(env.marker(t, darkRegion).IsDark())
	r.Empty(env.emails(t, operatorEmail))
	r.Len(env.emails(t, "alice@acme.com", email.TemplateRegionOffline), 1)

	entry, err := env.db.GetStateEntry(t.Context(), nil,
		watchdog.StateKeyPrefix+watchdog.DarkRegionFingerprint(darkRegion))
	r.NoError(err)
	r.Nil(entry)
}

// gaugeSeries gathers one gauge vector off a private registry, value per
// region label.
func gaugeSeries(t *testing.T, collector prometheus.Collector, name string) map[string]float64 {
	t.Helper()

	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(collector))

	families, err := reg.Gather()
	require.NoError(t, err)

	out := make(map[string]float64)

	for _, family := range families {
		if family.GetName() != name {
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

// TestSweepPublishesRegionGauges: solidping_workers_active and
// solidping_region_dark are written by every sweep, with the watchdog
// DISABLED — one series per cloud region, never a private label, and a
// region that vanished is dropped rather than frozen.
//
// Not parallel: the sweep resets and rewrites process-global gauges, so a
// concurrent sweep would race these reads. A non-parallel top-level test runs
// before any parallel test in the package is released.
//
//nolint:paralleltest // mutates process-global gauges
func TestSweepPublishesRegionGauges(t *testing.T) {
	r := require.New(t)
	env := newTestEnv(t)

	env.worker(t, "w-paris", healthyRegion)
	worker := env.strand(t)
	env.check(t, env.acme, "Pinned API", darkRegion)
	env.check(t, env.acme, "On-prem", "@datacenter")
	env.operator(t, false)

	prommetrics.SetWorkersActive("vanished-region", 3)
	env.sweep(t)

	r.InDelta(1.0, testutil.ToFloat64(prommetrics.WorkersActive.WithLabelValues(healthyRegion)), 0.001)
	r.InDelta(0.0, testutil.ToFloat64(prommetrics.WorkersActive.WithLabelValues(darkRegion)), 0.001)
	r.InDelta(1.0, testutil.ToFloat64(prommetrics.RegionDark.WithLabelValues(darkRegion)), 0.001)
	r.InDelta(0.0, testutil.ToFloat64(prommetrics.RegionDark.WithLabelValues(healthyRegion)), 0.001)

	for name, collector := range map[string]prometheus.Collector{
		"solidping_workers_active": prommetrics.WorkersActive,
		"solidping_region_dark":    prommetrics.RegionDark,
	} {
		series := gaugeSeries(t, collector, name)
		r.NotContains(series, "vanished-region")

		for region := range series {
			r.False(strings.HasPrefix(region, regions.PrivateRegionPrefix),
				"private region %q must never be a %s label", region, name)
		}
	}

	// The worker comes back: live 1, still dark for one more sweep (the
	// recovery needs two), then 0.
	r.NoError(env.db.UpdateWorkerHeartbeat(t.Context(), worker.UID, []string{}, ""))
	env.sweep(t)
	r.InDelta(1.0, testutil.ToFloat64(prommetrics.WorkersActive.WithLabelValues(darkRegion)), 0.001)
	r.InDelta(1.0, testutil.ToFloat64(prommetrics.RegionDark.WithLabelValues(darkRegion)), 0.001)

	env.sweep(t)
	r.InDelta(0.0, testutil.ToFloat64(prommetrics.RegionDark.WithLabelValues(darkRegion)), 0.001)
}

// TestFailedSweepLeavesGaugesAlone: a sweep whose region report failed knows
// nothing, so it must not reset the gauges to "0 workers everywhere".
//
//nolint:paralleltest // mutates process-global gauges
func TestFailedSweepLeavesGaugesAlone(t *testing.T) {
	r := require.New(t)
	env := newTestEnv(t)

	prommetrics.WorkersActive.Reset()
	prommetrics.SetWorkersActive(healthyRegion, 2)

	deps := env.deps()
	deps.Health = failingReporter{}

	_, err := regionsweep.Sweep(t.Context(), deps)
	r.Error(err)
	r.InDelta(2.0, testutil.ToFloat64(prommetrics.WorkersActive.WithLabelValues(healthyRegion)), 0.001)
}

// errFixture is the static error the failing reporter returns.
var errFixture = errors.New("region sweep test failure")

// failingReporter is a region health source that always fails.
type failingReporter struct{}

func (failingReporter) RegionHealthWithJobs(
	context.Context,
) (*checks.RegionHealthReport, []checks.RegionJob, error) {
	return nil, nil, errFixture
}
