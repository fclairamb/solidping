package watchdog_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/watchdog"
)

// markStale moves a check to the stale status the way the freshness sweep's
// guarded update does.
func (e *testEnv) markStale(t *testing.T, checkUID string) {
	t.Helper()

	check, err := e.db.GetCheck(t.Context(), e.org.UID, checkUID)
	require.NoError(t, err)

	changed, err := e.db.MarkCheckStale(t.Context(), checkUID, check.Status,
		time.Now().Add(time.Hour), time.Now())
	require.NoError(t, err)
	require.True(t, changed)
}

// The stale-checks detector reports stale checks whose region is ALIVE — a
// platform bug only the operator can fix — and stays silent about the ones a
// dark region already explains, so one outage is never reported twice (spec
// 2026-09-25-02 §6).
func TestStaleChecksDetector_ReportsOnlyChecksOutsideDarkRegions(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	realNow := time.Now()

	env.registerLiveWorker(t, "w-healthy", "healthy")

	// Stale in a live region: the scheduler/lease/rate-limit bug class.
	stuck := env.createCheck(t, []string{"healthy"})
	env.setJobScheduledAt(t, stuck.UID, realNow.Add(time.Hour))
	env.markStale(t, stuck.UID)

	// Stale in a dark region: explained by the dark-region detector.
	for range 6 {
		dead := env.createCheck(t, []string{"eu2"})
		env.setJobScheduledAt(t, dead.UID, realNow.Add(-3*time.Hour))
		env.markStale(t, dead.UID)
	}

	// Multi-region, one of them alive: still a platform bug.
	mixed := env.createCheck(t, []string{"eu2", "healthy"})
	env.markStale(t, mixed.UID)

	// A healthy, fresh check is never mentioned.
	env.createCheck(t, []string{"healthy"})

	report := env.watchdog.Evaluate(t.Context(), enabledConfig())
	r.Empty(report.Failed)

	r.NotNil(findAnomaly(report.Anomalies, watchdog.DetectorDarkRegion), "positive control: eu2 is dark")

	stale := findAnomaly(report.Anomalies, watchdog.DetectorStaleChecks)
	r.NotNil(stale)
	r.Equal(watchdog.SubjectStaleChecks, stale.Subject)
	r.Equal(2, stale.Count, "the six checks in the dark region are the dark-region detector's to report")
	r.Equal(2, report.StaleChecks)
	r.Equal(watchdog.SeverityWarning, stale.Severity)
	r.Contains(stale.Detail, "acme/")
}

// With no stale check, or only stale checks a dark region explains, the
// detector reports nothing.
func TestStaleChecksDetector_SilentWhenEverythingIsExplained(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	realNow := time.Now()

	for range 6 {
		dead := env.createCheck(t, []string{"eu2"})
		env.setJobScheduledAt(t, dead.UID, realNow.Add(-3*time.Hour))
		env.markStale(t, dead.UID)
	}

	report := env.watchdog.Evaluate(t.Context(), enabledConfig())
	r.Empty(report.Failed)
	r.Nil(findAnomaly(report.Anomalies, watchdog.DetectorStaleChecks))
	r.Zero(report.StaleChecks)

	check, err := env.db.GetCheck(t.Context(), env.org.UID, env.mustFirstCheck(t))
	r.NoError(err)
	r.Equal(models.CheckStatusStale, check.Status, "positive control: the checks really are stale")
}

func (e *testEnv) mustFirstCheck(t *testing.T) string {
	t.Helper()

	list, _, err := e.db.ListChecks(t.Context(), e.org.UID, &models.ListChecksFilter{})
	require.NoError(t, err)
	require.NotEmpty(t, list)

	return list[0].UID
}
