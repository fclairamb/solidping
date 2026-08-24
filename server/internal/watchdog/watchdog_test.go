package watchdog_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/watchdog"
)

// The watchdog suite is deliberately clock-pinned end to end. Two of its three
// detectors are defined against wall-clock windows ("the last completed hour",
// "the same hour a day earlier") and the anti-flood machine against a 24h
// re-notify window, so a test that read time.Now() would pass all day and fail
// during one particular minute of it.

// errFixture is the static error the failure fixtures wrap; a dynamic
// errors.New in a test body trips err113 just as it does in production code.
var errFixture = errors.New("watchdog test failure")

// pinnedNow is the instant every test evaluates at. It sits mid-hour on
// purpose: the fleet detector must exclude the still-filling current hour, and
// a top-of-the-hour `now` would hide a bug that only shows up mid-hour.
var pinnedNow = time.Date(2026, 8, 24, 4, 37, 12, 0, time.UTC) //nolint:gochecknoglobals // shared test fixture

// testEnv is one in-memory instance: a DB, one org, a real checks.Service
// (the spec-09 ghost detector the dark-region detector must call) and the
// watchdog under test.
type testEnv struct {
	db        *sqlite.Service
	org       *models.Organization
	checks    *checks.Service
	watchdog  *watchdog.Service
	nowHolder *time.Time
}

// newTestEnv builds a fresh instance with two declared regions.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	r.NoError(dbSvc.SetSystemParameter(ctx, regions.ParamRegions, []regions.RegionDefinition{
		{Slug: "healthy", Name: "Healthy"},
		{Slug: "eu2", Name: "EU 2"},
	}, false))

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	creds, err := credentials.NewService(nil, nil)
	r.NoError(err)

	entSvc := entcore.NewService(dbSvc, entcore.DefaultsFor(config.DeploymentModeSelfHosted), 0)
	checksSvc := checks.NewService(dbSvc, notifier.NewLocalEventNotifier(), creds, entSvc)

	now := pinnedNow
	env := &testEnv{db: dbSvc, org: org, checks: checksSvc, nowHolder: &now}

	env.watchdog = watchdog.NewService(dbSvc, checksSvc)
	env.watchdog.SetNow(func() time.Time { return *env.nowHolder })

	return env
}

// advance moves the pinned clock forward.
func (e *testEnv) advance(d time.Duration) {
	next := e.nowHolder.Add(d)
	*e.nowHolder = next
}

// enabledConfig is the watchdog turned on with defaults.
func enabledConfig() *watchdog.Config {
	cfg := watchdog.DefaultConfig()
	cfg.Enabled = true

	return cfg
}

// createCheck creates one check pinned to the given regions, through the real
// checks service so its check_jobs are materialized exactly as production
// does.
func (e *testEnv) createCheck(t *testing.T, regionSlugs []string) checks.CheckResponse {
	t.Helper()

	resp, err := e.checks.CreateCheck(t.Context(), e.org.Slug, checks.CreateCheckRequest{
		Type:    "http",
		Config:  map[string]any{"url": "https://example.com"},
		Regions: regionSlugs,
		Period:  ptr("00:01:00"),
	})
	require.NoError(t, err)

	return resp
}

// setJobScheduledAt back-dates every job of a check, which is how a test makes
// jobs overdue without racing the clock.
func (e *testEnv) setJobScheduledAt(t *testing.T, checkUID string, at time.Time) {
	t.Helper()

	_, err := e.db.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", at).
		Where("check_uid = ?", checkUID).
		Exec(t.Context())
	require.NoError(t, err)
}

// addResults inserts n raw results for a check, all stamped at `at`.
func (e *testEnv) addResults(t *testing.T, checkUID string, at time.Time, n int) {
	t.Helper()

	for range n {
		result := models.NewResult(e.org.UID, checkUID, models.ResultStatusUp, 12)
		result.PeriodStart = at
		result.CreatedAt = at
		require.NoError(t, e.db.CreateResult(t.Context(), result))
	}
}

func ptr[T any](v T) *T { return &v }

// failingRegionHealth is the reporter that always errors — the fixture that
// proves one broken detector cannot silence the others.
type failingRegionHealth struct{ err error }

func (f failingRegionHealth) RegionHealth(_ context.Context) (*checks.RegionHealthReport, error) {
	return nil, f.err
}

// findAnomaly returns the anomaly for a detector, or nil.
func findAnomaly(anomalies []watchdog.Anomaly, detector string) *watchdog.Anomaly {
	for i := range anomalies {
		if anomalies[i].Detector == detector {
			return &anomalies[i]
		}
	}

	return nil
}

// TestDarkRegionDetectorReproducesTheStranding is the 2026-08-24 shape: jobs
// carrying a region slug that no live worker prefix-matches, overdue by hours.
// It runs through the REAL checks.Service, so what it proves is that the
// watchdog's notion of "dark" is spec-09's and not a second copy.
func TestDarkRegionDetectorReproducesTheStranding(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	// Region fixtures are built against the real clock: RegionHealth reads
	// its own, and the dark-region detector measures the age bar against the
	// report's GeneratedAt, so both sides agree by construction. Three hours
	// is far enough from any bar that no wall-clock phase can flip it.
	realNow := time.Now()

	// A healthy region: live worker, jobs not yet due.
	healthy := env.createCheck(t, []string{"healthy"})
	env.setJobScheduledAt(t, healthy.UID, realNow.Add(time.Hour))
	env.registerLiveWorker(t, "w-healthy", "healthy")

	// The stranded region: six checks pointed at a slug nothing serves, every
	// job three hours overdue.
	for range 6 {
		stranded := env.createCheck(t, []string{"eu2"})
		env.setJobScheduledAt(t, stranded.UID, realNow.Add(-3*time.Hour))
	}

	report := env.watchdog.Evaluate(t.Context(), enabledConfig())
	r.Empty(report.Failed)

	dark := findAnomaly(report.Anomalies, watchdog.DetectorDarkRegion)
	r.NotNil(dark, "a slug with six jobs and no live worker must be reported")
	r.Equal("eu2", dark.Subject)
	r.Equal(watchdog.SeverityCritical, dark.Severity,
		"three hours overdue is past the critical-age bar")
	r.Equal(6, dark.Count)
	r.Equal(6, report.StrandedJobs)
	r.Contains(dark.Headline, "DARK")
	r.Contains(dark.Remediation, "/system/regions/migrate",
		"the digest must carry the ready-to-run remediation from spec 2026-08-24-08")

	// The healthy region must not appear at all.
	for _, anomaly := range report.Anomalies {
		r.NotEqual("healthy", anomaly.Subject)
	}
}

// TestDarkRegionDetectorRespectsTheBlastRadiusBar pins the "1 job 90 seconds
// late is not an anomaly" half of the rule — the half that decides whether an
// operator keeps the watchdog switched on.
func TestDarkRegionDetectorRespectsTheBlastRadiusBar(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name        string
		checks      int
		overdueBy   time.Duration
		wantAnomaly bool
	}{
		{"one job barely late", 1, 90 * time.Second, false},
		{"enough jobs but barely late", 6, 90 * time.Second, false},
		{"old enough but too few jobs", 4, 3 * time.Hour, false},
		{"wide and old", 5, 11 * time.Minute, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := newTestEnv(t)

			realNow := time.Now()

			for range testCase.checks {
				check := env.createCheck(t, []string{"eu2"})
				env.setJobScheduledAt(t, check.UID, realNow.Add(-testCase.overdueBy))
			}

			report := env.watchdog.Evaluate(t.Context(), enabledConfig())
			r.Empty(report.Failed)

			dark := findAnomaly(report.Anomalies, watchdog.DetectorDarkRegion)
			if testCase.wantAnomaly {
				r.NotNil(dark)
			} else {
				r.Nil(dark)
			}
		})
	}
}

// TestFleetCollapseDetector halves the produced-result rate against the same
// hour a day earlier, and pins the floor that keeps a quiet instance silent.
func TestFleetCollapseDetector(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name         string
		baseline     int
		current      int
		wantAnomaly  bool
		wantSeverity watchdog.Severity
	}{
		{"collapsed hard", 400, 40, true, watchdog.SeverityCritical},
		{"halved just past the bar", 400, 190, true, watchdog.SeverityWarning},
		{"exactly at the drop bar is not an anomaly", 400, 200, false, 0},
		{"collapsed but below the baseline floor", 40, 1, false, 0},
		{"steady", 400, 410, false, 0},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := newTestEnv(t)

			check := env.createCheck(t, []string{"healthy"})

			// The last COMPLETED hour is [03:00, 04:00) for a 04:37 `now`.
			lastHour := pinnedNow.Truncate(time.Hour).Add(-time.Hour)

			env.addResults(t, check.UID, lastHour.Add(10*time.Minute), testCase.current)
			env.addResults(t, check.UID, lastHour.AddDate(0, 0, -1).Add(10*time.Minute), testCase.baseline)

			// Rows inside the CURRENT, still-filling hour must be ignored;
			// counting them would make every run before :59 read as a
			// collapse.
			env.addResults(t, check.UID, pinnedNow.Truncate(time.Hour).Add(time.Minute), 5000)

			report := env.watchdog.Evaluate(t.Context(), enabledConfig())
			r.Empty(report.Failed)

			collapse := findAnomaly(report.Anomalies, watchdog.DetectorFleetCollapse)
			if !testCase.wantAnomaly {
				r.Nil(collapse)

				return
			}

			r.NotNil(collapse)
			r.Equal(watchdog.SubjectFleet, collapse.Subject)
			r.Equal(testCase.wantSeverity, collapse.Severity)
			r.Equal(testCase.baseline-testCase.current, collapse.Count)
		})
	}
}

// TestStaleIncidentDetector reproduces the frozen incident: an active incident
// whose check has produced nothing for hours — including the exact shape that
// made 2026-08-24 expensive, a target that had already recovered while the
// incident stayed open because nothing was executing the check.
func TestStaleIncidentDetector(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	frozen := env.createCheck(t, []string{"eu2"})
	fresh := env.createCheck(t, []string{"healthy"})

	env.clearResults(t, frozen.UID)
	env.clearResults(t, fresh.UID)

	// The frozen check last produced a result three hours ago; its incident
	// opened before that and has never been touched since.
	env.addResults(t, frozen.UID, pinnedNow.Add(-3*time.Hour), 1)
	env.openIncident(t, frozen.UID, pinnedNow.Add(-4*time.Hour), 1)

	// The fresh check is still executing every minute — its open incident is
	// a REAL, live outage and must never be reported as frozen.
	env.addResults(t, fresh.UID, pinnedNow.Add(-30*time.Second), 1)
	env.openIncident(t, fresh.UID, pinnedNow.Add(-20*time.Minute), 2)

	report := env.watchdog.Evaluate(t.Context(), enabledConfig())
	r.Empty(report.Failed)

	stale := findAnomaly(report.Anomalies, watchdog.DetectorStaleIncidents)
	r.NotNil(stale)
	r.Equal(1, stale.Count, "only the frozen incident counts")
	r.Equal(1, report.StaleIncidents)
	r.Equal(watchdog.SeverityWarning, stale.Severity)
	r.Contains(stale.Detail, "#1", "the digest names the oldest offenders")
	r.NotContains(stale.Detail, "#2")
}

// TestStaleIncidentDetectorIgnoresDisabledChecks: a check nobody executes on
// purpose has no business being reported as one that mysteriously stopped.
func TestStaleIncidentDetectorIgnoresDisabledChecks(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	check := env.createCheck(t, []string{"eu2"})
	env.clearResults(t, check.UID)
	env.addResults(t, check.UID, pinnedNow.Add(-3*time.Hour), 1)
	env.openIncident(t, check.UID, pinnedNow.Add(-4*time.Hour), 1)

	_, err := env.db.DB().NewUpdate().
		Model((*models.Check)(nil)).
		Set("enabled = ?", false).
		Where("uid = ?", check.UID).
		Exec(t.Context())
	r.NoError(err)

	report := env.watchdog.Evaluate(t.Context(), enabledConfig())
	r.Empty(report.Failed)
	r.Nil(findAnomaly(report.Anomalies, watchdog.DetectorStaleIncidents))
}

// TestDetectorFailureDoesNotSuppressTheOthers is the independence contract: a
// watchdog that goes quiet because one query broke reproduces the exact
// failure mode it exists to catch.
func TestDetectorFailureDoesNotSuppressTheOthers(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newTestEnv(t)

	boom := fmt.Errorf("region health exploded: %w", errFixture)

	broken := watchdog.NewService(env.db, failingRegionHealth{err: boom})
	broken.SetNow(func() time.Time { return pinnedNow })

	// Set up a fleet collapse AND a frozen incident, so both surviving
	// detectors have something to report.
	check := env.createCheck(t, []string{"healthy"})
	env.clearResults(t, check.UID)

	lastHour := pinnedNow.Truncate(time.Hour).Add(-time.Hour)
	env.addResults(t, check.UID, lastHour.AddDate(0, 0, -1), 400)
	env.addResults(t, check.UID, lastHour, 10)
	env.openIncident(t, check.UID, pinnedNow.Add(-4*time.Hour), 1)

	report := broken.Evaluate(t.Context(), enabledConfig())

	r.Len(report.Failed, 1)
	r.ErrorIs(report.Failed[watchdog.DetectorDarkRegion], boom)
	r.False(report.DetectorSucceeded(watchdog.DetectorDarkRegion))
	r.True(report.DetectorSucceeded(watchdog.DetectorFleetCollapse))

	r.NotNil(findAnomaly(report.Anomalies, watchdog.DetectorFleetCollapse),
		"the fleet detector must still report while the region detector is broken")
	r.NotNil(findAnomaly(report.Anomalies, watchdog.DetectorStaleIncidents),
		"the incident detector must still report while the region detector is broken")
}
