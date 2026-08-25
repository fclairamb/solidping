package watchdog_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/watchdog"
)

// darkAnomaly is a synthetic dark-region anomaly. The transition machine is
// tested against synthetic anomalies on purpose: what is under test here is
// the new → silent → re-notify → recovered ladder, not the detectors that
// feed it.
func darkAnomaly(severity watchdog.Severity) watchdog.Anomaly {
	return watchdog.Anomaly{
		Detector: watchdog.DetectorDarkRegion,
		Subject:  "eu2",
		Severity: severity,
		Headline: "region \"eu2\" is DARK",
		Count:    345,
	}
}

// reportOf builds a report with the given anomalies and no detector failures.
func reportOf(at time.Time, anomalies ...watchdog.Anomaly) *watchdog.Report {
	return &watchdog.Report{
		GeneratedAt: at,
		Anomalies:   anomalies,
		Failed:      map[string]error{},
	}
}

// kindsByFingerprint indexes the transitions of one run.
func kindsByFingerprint(transitions []watchdog.Transition) map[string]watchdog.Transition {
	out := make(map[string]watchdog.Transition, len(transitions))
	for _, transition := range transitions {
		out[transition.Fingerprint] = transition
	}

	return out
}

// TestTransitionLadder walks the full anti-flood contract in one sequence,
// because the interesting property is the SEQUENCE: each step's correctness
// depends on what the previous step persisted.
func TestTransitionLadder(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)
	cfg := enabledConfig()
	ctx := t.Context()

	// 1. First sighting → notify.
	first, err := env.watchdog.Reconcile(ctx, reportOf(*env.nowHolder, darkAnomaly(watchdog.SeverityCritical)), cfg)
	r.NoError(err)
	r.Len(first, 1)
	r.Equal(watchdog.TransitionNew, first[0].Kind)
	r.True(first[0].Notify)

	firstSeen := first[0].FirstSeenAt

	// 2. Still broken an hour later → SILENT. This is the property that keeps
	// a three-hour outage from producing three identical pages.
	env.advance(time.Hour)

	second, err := env.watchdog.Reconcile(ctx, reportOf(*env.nowHolder, darkAnomaly(watchdog.SeverityCritical)), cfg)
	r.NoError(err)
	r.Len(second, 1)
	r.Equal(watchdog.TransitionOngoing, second[0].Kind)
	r.False(second[0].Notify)
	r.Equal(firstSeen.UTC(), second[0].FirstSeenAt.UTC(), "first-seen must survive the silent run")
	r.Empty(watchdog.BuildDigest(second, reportOf(*env.nowHolder), *env.nowHolder).Subject,
		"a fully silent run must produce no digest at all")

	// 3. A run at 23h is still inside the window.
	env.advance(22 * time.Hour)

	third, err := env.watchdog.Reconcile(ctx, reportOf(*env.nowHolder, darkAnomaly(watchdog.SeverityCritical)), cfg)
	r.NoError(err)
	r.Equal(watchdog.TransitionOngoing, third[0].Kind)
	r.False(third[0].Notify)

	// 4. Past 24h since the last notification → re-notify, as "still broken
	// since …".
	env.advance(2 * time.Hour)

	fourth, err := env.watchdog.Reconcile(ctx, reportOf(*env.nowHolder, darkAnomaly(watchdog.SeverityCritical)), cfg)
	r.NoError(err)
	r.Equal(watchdog.TransitionRenotify, fourth[0].Kind)
	r.True(fourth[0].Notify)
	r.Equal(firstSeen.UTC(), fourth[0].FirstSeenAt.UTC())
	r.Contains(watchdog.BuildDigest(fourth, reportOf(*env.nowHolder), *env.nowHolder).Text, "STILL BROKEN since")

	// 5. Condition gone → recovery notice, exactly once.
	env.advance(time.Hour)

	fifth, err := env.watchdog.Reconcile(ctx, reportOf(*env.nowHolder), cfg)
	r.NoError(err)
	r.Len(fifth, 1)
	r.Equal(watchdog.TransitionResolved, fifth[0].Kind)
	r.True(fifth[0].Notify)
	r.Equal(firstSeen.UTC(), fifth[0].FirstSeenAt.UTC(),
		"the recovery notice dates the outage it closes")

	// 6. The next clean run says nothing — the marker is gone.
	env.advance(time.Hour)

	sixth, err := env.watchdog.Reconcile(ctx, reportOf(*env.nowHolder), cfg)
	r.NoError(err)
	r.Empty(sixth, "a recovery must be announced exactly once")
}

// TestOngoingAnomalyRenotifiesOnEscalation: "still 5 jobs late" and "the whole
// region just went dark" are not the same page, so an escalation breaks the
// quiet window.
func TestOngoingAnomalyRenotifiesOnEscalation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)
	cfg := enabledConfig()
	ctx := t.Context()

	_, err := env.watchdog.Reconcile(ctx, reportOf(*env.nowHolder, darkAnomaly(watchdog.SeverityWarning)), cfg)
	r.NoError(err)

	env.advance(time.Hour)

	same, err := env.watchdog.Reconcile(ctx, reportOf(*env.nowHolder, darkAnomaly(watchdog.SeverityWarning)), cfg)
	r.NoError(err)
	r.False(same[0].Notify, "an unchanged warning stays silent inside the window")

	env.advance(time.Hour)

	escalated, err := env.watchdog.Reconcile(ctx, reportOf(*env.nowHolder, darkAnomaly(watchdog.SeverityCritical)), cfg)
	r.NoError(err)
	r.Equal(watchdog.TransitionRenotify, escalated[0].Kind)
	r.True(escalated[0].Notify)
}

// TestFailedDetectorNeverResolvesItsOwnAnomalies is the load-bearing guard: a
// broken query must not be laundered into "recovered". Telling an operator the
// outage is over when the watchdog simply could not look is worse than saying
// nothing.
func TestFailedDetectorNeverResolvesItsOwnAnomalies(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)
	cfg := enabledConfig()
	ctx := t.Context()

	_, err := env.watchdog.Reconcile(ctx, reportOf(*env.nowHolder, darkAnomaly(watchdog.SeverityCritical)), cfg)
	r.NoError(err)

	env.advance(time.Hour)

	// The dark-region detector broke; the others ran clean.
	broken := reportOf(*env.nowHolder)
	broken.Failed[watchdog.DetectorDarkRegion] = fmt.Errorf("query failed: %w", errFixture)

	transitions, err := env.watchdog.Reconcile(ctx, broken, cfg)
	r.NoError(err)
	r.Empty(transitions, "a broken detector must produce neither an anomaly nor a recovery")

	// And once the detector works again and the condition is genuinely gone,
	// the recovery finally fires — the marker was preserved, not dropped.
	env.advance(time.Hour)

	recovered, err := env.watchdog.Reconcile(ctx, reportOf(*env.nowHolder), cfg)
	r.NoError(err)
	r.Len(recovered, 1)
	r.Equal(watchdog.TransitionResolved, recovered[0].Kind)
}

// TestReconcileTracksEachFingerprintIndependently: two anomalies from
// different detectors must not share an anti-flood window.
func TestReconcileTracksEachFingerprintIndependently(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)
	cfg := enabledConfig()
	ctx := t.Context()

	fleet := watchdog.Anomaly{
		Detector: watchdog.DetectorFleetCollapse,
		Subject:  watchdog.SubjectFleet,
		Severity: watchdog.SeverityCritical,
		Headline: "fleet execution collapsed",
	}

	_, err := env.watchdog.Reconcile(ctx, reportOf(*env.nowHolder, darkAnomaly(watchdog.SeverityCritical)), cfg)
	r.NoError(err)

	env.advance(time.Hour)

	mixed, err := env.watchdog.Reconcile(
		ctx, reportOf(*env.nowHolder, darkAnomaly(watchdog.SeverityCritical), fleet), cfg)
	r.NoError(err)

	byFingerprint := kindsByFingerprint(mixed)
	r.Equal(watchdog.TransitionOngoing, byFingerprint["dark-region:eu2"].Kind)
	r.False(byFingerprint["dark-region:eu2"].Notify)
	r.Equal(watchdog.TransitionNew, byFingerprint["fleet-collapse:instance"].Kind)
	r.True(byFingerprint["fleet-collapse:instance"].Notify)
}

// TestMinSeverityFiltersDeliveryNotDetection: an anomaly below the bar must
// not be delivered AND must not leave a marker claiming it was — otherwise the
// real page, when it escalates past the bar, would be suppressed as "ongoing".
func TestMinSeverityFiltersDeliveryNotDetection(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)
	ctx := t.Context()

	cfg := enabledConfig()
	cfg.MinSeverity = "critical"

	report := reportOf(*env.nowHolder, darkAnomaly(watchdog.SeverityWarning))

	transitions, err := env.watchdog.Reconcile(ctx, report.Filtered(cfg.Severity()), cfg)
	r.NoError(err)
	r.Empty(transitions, "a below-bar anomaly is logged and metered, never delivered")

	// It escalates: now it is above the bar and must read as NEW, not as a
	// silently-ongoing anomaly.
	env.advance(time.Hour)

	escalated := reportOf(*env.nowHolder, darkAnomaly(watchdog.SeverityCritical))

	transitions, err = env.watchdog.Reconcile(ctx, escalated.Filtered(cfg.Severity()), cfg)
	r.NoError(err)
	r.Len(transitions, 1)
	r.Equal(watchdog.TransitionNew, transitions[0].Kind)
	r.True(transitions[0].Notify)
}
