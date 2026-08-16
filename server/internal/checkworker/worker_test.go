package checkworker

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

func setupTestRunner(t *testing.T) (*CheckWorker, *sqlite.Service, context.Context) {
	t.Helper()

	ctx := context.Background()

	// Create in-memory database
	svc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err, "failed to create in-memory database")

	err = svc.Initialize(ctx)
	require.NoError(t, err, "failed to initialize database")

	// Create test config
	cfg := &config.Config{
		Server: config.ServerConfig{
			CheckWorker: config.CheckWorkerConfig{
				Nb:            5,
				FetchMaxAhead: 5 * time.Minute,
			},
		},
	}

	// Create services
	svcList := services.NewRegistry()
	checkJobSvc := checkjobsvc.NewService(svc.DB())
	svcList.CheckJobs = checkJobSvc

	// Create event notifier for tests
	eventNotifier := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = eventNotifier.Close() })
	svcList.EventNotifier = eventNotifier

	// Create runner
	runner := NewCheckWorker(
		svc,
		cfg,
		svcList,
		checkJobSvc,
	)

	return runner, svc, ctx
}

// TestCalculateNextScheduledAt_NoAttachedCheck exercises the defensive
// fallback path (spec 2026-07-05-08 D1): when checkJob.Check is nil (should
// not happen via the regular claim path, but backend/direct.go and
// handlers/workers/service.go — the remote-worker submit-result paths — have
// their own calculateNextScheduledAt copies operating on a bare job with no
// attached check, so this fallback documents/preserves their exact
// pre-existing "on schedule keep phase / behind schedule catch up to now"
// semantics for any caller in the same situation).
//
//nolint:paralleltest // Subtests share runner and time reference
func TestCalculateNextScheduledAt_NoAttachedCheck(t *testing.T) {
	runner, dbSvc, _ := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	now := time.Now()

	t.Run("OnSchedule", func(t *testing.T) { //nolint:paralleltest // Shares runner instance
		// Job scheduled 10 seconds ago with 1 minute period
		// Next should be scheduled_at + period (50 seconds from now)
		scheduledAt := now.Add(-10 * time.Second)
		checkJob := &models.CheckJob{
			ScheduledAt: &scheduledAt,
			Period:      timeutils.Duration(time.Minute), // 1 minute
		}

		nextScheduled := runner.calculateNextScheduledAt(checkJob)

		expected := scheduledAt.Add(1 * time.Minute)
		assert.WithinDuration(t, expected, nextScheduled, 1*time.Second)
		assert.True(t, nextScheduled.After(now), "next scheduled should be in the future")
	})

	t.Run("BehindSchedule", func(t *testing.T) { //nolint:paralleltest // Shares runner instance
		// Job scheduled 2 minutes ago with 1 minute period
		// Next should be now + period
		scheduledAt := now.Add(-2 * time.Minute)
		checkJob := &models.CheckJob{
			ScheduledAt: &scheduledAt,
			Period:      timeutils.Duration(time.Minute), // 1 minute
		}

		nextScheduled := runner.calculateNextScheduledAt(checkJob)

		expected := now.Add(1 * time.Minute)
		assert.WithinDuration(t, expected, nextScheduled, 2*time.Second)
	})

	t.Run("NoScheduledAt", func(t *testing.T) { //nolint:paralleltest // Shares runner instance
		checkJob := &models.CheckJob{
			ScheduledAt: nil,
			Period:      timeutils.Duration(5 * time.Minute), // 5 minutes
		}

		nextScheduled := runner.calculateNextScheduledAt(checkJob)

		expected := now.Add(5 * time.Minute)
		assert.WithinDuration(t, expected, nextScheduled, 2*time.Second)
	})

	t.Run("DifferentPeriods", func(t *testing.T) {
		scheduledAt := now.Add(-10 * time.Second)

		testCases := []struct {
			name   string
			period time.Duration
		}{
			{"30s", 30 * time.Second},
			{"1m", 1 * time.Minute},
			{"5m", 5 * time.Minute},
			{"1h", 1 * time.Hour},
			// 336h (2 weeks) — domain's new long-interval option (spec
			// 2026-08-15-07) — proves the reschedule math isn't silently
			// truncated once the period is measured in weeks.
			{"336h", 336 * time.Hour},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				checkJob := &models.CheckJob{
					ScheduledAt: &scheduledAt,
					Period:      timeutils.Duration(tc.period),
				}

				nextScheduled := runner.calculateNextScheduledAt(checkJob)

				expected := scheduledAt.Add(tc.period)
				assert.WithinDuration(t, expected, nextScheduled, 1*time.Second)
			})
		}
	})
}

// TestCalculateNextScheduledAt_PhaseLocked exercises the D1 phase-locked
// path: when checkJob.Check is attached (the normal case for jobs flowing
// through ClaimJobs/ClaimJobsForCheck), rescheduling aligns to the job's
// deterministic phase instead of preserving/losing the literal scheduled_at
// anchor. This is what fixes F2 (lockstep after a late run / restart).
//
//nolint:paralleltest // Subtests share runner and time reference
func TestCalculateNextScheduledAt_PhaseLocked(t *testing.T) {
	runner, dbSvc, _ := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	checkUID := "e2e55fa2-1719-49c3-b3f9-af0682db3b55"
	regions := []string{"us-1", "eu-2", "default"}

	regionOf := func(s string) *string { return &s }

	newJobWithCheck := func(scheduledAt *time.Time, jobPeriod, basePeriod time.Duration, region *string) *models.CheckJob {
		return &models.CheckJob{
			CheckUID:    checkUID,
			Region:      region,
			ScheduledAt: scheduledAt,
			Period:      timeutils.Duration(jobPeriod),
			Check: &models.Check{
				UID:     checkUID,
				Period:  timeutils.Duration(basePeriod),
				Regions: regions,
			},
		}
	}

	t.Run("OnTime_MatchesPureFunction", func(t *testing.T) { //nolint:paralleltest // Shares runner instance
		now := time.Now()
		scheduledAt := now.Add(-10 * time.Second)
		basePeriod := time.Minute
		jobPeriod := 3 * time.Minute

		checkJob := newJobWithCheck(&scheduledAt, jobPeriod, basePeriod, regionOf("eu-2"))

		got := runner.calculateNextScheduledAt(checkJob)
		after := time.Now()
		want := scheduling.NextAligned(after, basePeriod, jobPeriod, checkUID, regionOf("eu-2"), regions,
			scheduling.RegionSpread(basePeriod, len(regions), nil))

		// The method reads its own clock, so `want` (from a later sample) can
		// land one full period after `got` if the phase-aligned second ticks
		// between the two reads — a fixed small tolerance would flake there.
		// Delegation to the pure function is proven by phase equality (exact,
		// boundary-immune) plus a within-one-period distance bound.
		phaseOf := func(tt time.Time) int64 { return tt.Unix() % int64(jobPeriod/time.Second) }
		assert.Equal(t, phaseOf(want), phaseOf(got), "method must land on the pure function's phase")
		assert.LessOrEqual(t, (got.Sub(want)).Abs(), jobPeriod,
			"method and pure function may differ by at most one period (boundary tick between clock reads)")
		assert.True(t, got.After(now))
		assert.LessOrEqual(t, got.Sub(after), jobPeriod, "next tick is at most one job period out")
	})

	t.Run("LateRun_ResumesOnPhase_NotNowPlusPeriod", func(t *testing.T) { //nolint:paralleltest // Shares runner instance
		basePeriod := time.Minute
		jobPeriod := 3 * time.Minute
		region := regionOf("default")

		// Compute the phase this job should have landed on.
		refNow := time.Now()
		refNext := scheduling.NextAligned(refNow, basePeriod, jobPeriod, checkUID, region, regions,
			scheduling.RegionSpread(basePeriod, len(regions), nil))

		// Simulate a very-late release (well past scheduled_at, e.g. after a
		// restart) — the OLD behavior would return now+period, losing phase.
		veryLateScheduledAt := refNow.Add(-10 * time.Minute)
		checkJob := newJobWithCheck(&veryLateScheduledAt, jobPeriod, basePeriod, region)

		got := runner.calculateNextScheduledAt(checkJob)

		phaseOf := func(t time.Time) int64 { return t.Unix() % int64(jobPeriod/time.Second) }
		assert.Equal(t, phaseOf(refNext), phaseOf(got),
			"a late run must resume on the deterministic phase, not drift via now+period")
	})

	t.Run("NoRegionMatch_FallsBackToIndexZero", func(t *testing.T) { //nolint:paralleltest // Shares runner instance
		now := time.Now()
		basePeriod := time.Minute
		jobPeriod := 3 * time.Minute
		scheduledAt := now.Add(-10 * time.Second)

		// Job's region ("ap-3") no longer exists in check.Regions — stale job
		// about to be reconciled away. Must not panic; degrades to i=0.
		checkJob := newJobWithCheck(&scheduledAt, jobPeriod, basePeriod, regionOf("ap-3"))

		got := runner.calculateNextScheduledAt(checkJob)
		assert.True(t, got.After(now))
	})

	t.Run("NoRegionJob_JitterOnly", func(t *testing.T) { //nolint:paralleltest // Shares runner instance
		now := time.Now()
		basePeriod := time.Minute
		scheduledAt := now.Add(-5 * time.Second)

		checkJob := &models.CheckJob{
			CheckUID:    checkUID,
			Region:      nil,
			ScheduledAt: &scheduledAt,
			Period:      timeutils.Duration(basePeriod),
			Check: &models.Check{
				UID:    checkUID,
				Period: timeutils.Duration(basePeriod),
				// No regions on the check at all.
			},
		}

		got := runner.calculateNextScheduledAt(checkJob)
		after := time.Now()

		assert.True(t, got.After(now))
		// NextAligned guarantees next − now ∈ [1s, period] against the clock
		// the method itself reads (worker.go), which is strictly later than
		// this test's `now`. When the call lands inside the check's phase
		// second (fnv64a(checkUID) % 60s truncates to :30 for this UID), the
		// result is a full period after the internal clock — measured from the
		// earlier `now` that exceeds basePeriod by the nanoseconds between the
		// two clock reads, so the test failed during second :30 of any minute.
		// Bound against a clock sampled after the call: exact in every second.
		assert.LessOrEqual(t, got.Sub(after), basePeriod)
	})

	t.Run("JitterDeterminism_DiffChecksGetDiffPhases", func(t *testing.T) { //nolint:paralleltest // shares runner
		now := time.Now()
		basePeriod := time.Minute
		scheduledAt := now.Add(-1 * time.Second)

		jobA := &models.CheckJob{
			CheckUID: "check-aaaa", ScheduledAt: &scheduledAt, Period: timeutils.Duration(basePeriod),
			Check: &models.Check{UID: "check-aaaa", Period: timeutils.Duration(basePeriod)},
		}
		jobB := &models.CheckJob{
			CheckUID: "check-bbbb", ScheduledAt: &scheduledAt, Period: timeutils.Duration(basePeriod),
			Check: &models.Check{UID: "check-bbbb", Period: timeutils.Duration(basePeriod)},
		}

		nextA1 := runner.calculateNextScheduledAt(jobA)
		nextA2 := runner.calculateNextScheduledAt(jobA)
		nextB := runner.calculateNextScheduledAt(jobB)

		// Determinism means "same phase", not "same instant": if check-aaaa's
		// phase second ticks between the two calls, nextA2 is a full period
		// after nextA1 and a small WithinDuration tolerance would flake.
		phaseOf := func(tt time.Time) int64 { return tt.Unix() % int64(basePeriod/time.Second) }
		assert.Equal(t, phaseOf(nextA1), phaseOf(nextA2), "same check must land on the same phase across calls")
		assert.LessOrEqual(t, (nextA2.Sub(nextA1)).Abs(), basePeriod,
			"adjacent calls may differ by at most one period (boundary tick between clock reads)")
		_ = nextB // different UID may or may not collide; determinism (not spread) is what's load-bearing here
	})
}

// TestDelaySampleMs verifies the telemetry semantics of the delay sample
// (spec 2026-07-01-02 D4): lateness is measured against the job's real
// scheduled_at — the schedule the user configured — not the cost-padded
// effective_scheduled_at, and is floored at 0 (a probe that starts on time or
// is claimed ahead yields 0).
func TestDelaySampleMs(t *testing.T) {
	t.Parallel()

	// The sample itself is pure math shared with the server-side accounting
	// path (spec 2026-07-27-01), so it is exercised through scheduling.
	delaySampleMs := func(job *models.CheckJob, execStart time.Time) float64 {
		return scheduling.DelaySampleMs(job.ScheduledAt, job.EffectiveScheduledAt, execStart)
	}
	now := time.Now()

	t.Run("OnTimeStartYieldsZero", func(t *testing.T) {
		t.Parallel()

		scheduledAt := now
		job := &models.CheckJob{ScheduledAt: &scheduledAt, EffectiveScheduledAt: &scheduledAt}
		require.Zero(t, delaySampleMs(job, now), "a probe starting exactly on schedule has 0 delay")
	})

	t.Run("EarlyStartFlooredAtZero", func(t *testing.T) {
		t.Parallel()

		scheduledAt := now.Add(10 * time.Second)
		job := &models.CheckJob{ScheduledAt: &scheduledAt}
		require.Zero(t, delaySampleMs(job, now), "a probe starting before schedule is floored at 0")
	})

	t.Run("MeasuresAgainstScheduledAtNotEffective", func(t *testing.T) {
		t.Parallel()

		// The effective deadline is padded 20s past the schedule; the probe
		// starts 5s after the real schedule. True lateness is 5s — the padded
		// deadline must not absorb it (that under-reporting was the delay-EWMA
		// feedback loop this spec removes).
		scheduledAt := now.Add(-5 * time.Second)
		effective := now.Add(15 * time.Second)
		job := &models.CheckJob{ScheduledAt: &scheduledAt, EffectiveScheduledAt: &effective}
		require.InDelta(t, 5000.0, delaySampleMs(job, now), 1.0,
			"delay must be measured against scheduled_at, not effective_scheduled_at")
	})

	t.Run("NilScheduledAtFallsBackToEffective", func(t *testing.T) {
		t.Parallel()

		effective := now.Add(-3 * time.Second)
		job := &models.CheckJob{ScheduledAt: nil, EffectiveScheduledAt: &effective}
		require.InDelta(t, 3000.0, delaySampleMs(job, now), 1.0,
			"without a scheduled_at, the effective deadline is the fallback reference")
	})

	t.Run("NoReferenceYieldsZero", func(t *testing.T) {
		t.Parallel()

		job := &models.CheckJob{}
		require.Zero(t, delaySampleMs(job, now))
	})
}

// TestWaitAndDelay covers the D4 wait/delay separation (spec
// 2026-07-05-08): the "Check job completed" log line must report
// duration_ms as the actual exec time, with the pre-schedule sleep and the
// past-due dispatch delay broken out into their own fields instead of all
// three being folded into one conflated number.
func TestWaitAndDelay(t *testing.T) {
	t.Parallel()

	t.Run("ClaimedAheadOfSchedule_YieldsWaitOnly", func(t *testing.T) {
		t.Parallel()

		wait, delay := waitAndDelay(45 * time.Second)
		assert.Equal(t, 45*time.Second, wait, "positive sleepTime is the observed pre-schedule wait")
		assert.Zero(t, delay)
	})

	t.Run("ClaimedLate_YieldsDelayOnly", func(t *testing.T) {
		t.Parallel()

		wait, delay := waitAndDelay(-7 * time.Second)
		assert.Zero(t, wait)
		assert.Equal(t, 7*time.Second, delay, "negative sleepTime becomes a positive past-due delay")
	})

	t.Run("ExactlyOnSchedule_YieldsNeither", func(t *testing.T) {
		t.Parallel()

		wait, delay := waitAndDelay(0)
		assert.Zero(t, wait)
		assert.Zero(t, delay)
	})
}

func TestFormatISO8601Duration(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{"Zero", 0, "PT0S"},
		{"1 second", 1 * time.Second, "PT1S"},
		{"5 seconds", 5 * time.Second, "PT5S"},
		{"15 seconds", 15 * time.Second, "PT15S"},
		{"30 seconds", 30 * time.Second, "PT30S"},
		{"1 minute", 1 * time.Minute, "PT1M"},
		{"5 minutes", 5 * time.Minute, "PT5M"},
		{"1 hour", 1 * time.Hour, "PT1H"},
		{"1 hour 30 minutes", 90 * time.Minute, "PT1H30M"},
		{"1 hour 30 minutes 45 seconds", 1*time.Hour + 30*time.Minute + 45*time.Second, "PT1H30M45S"},
		{"2 hours 15 minutes", 2*time.Hour + 15*time.Minute, "PT2H15M"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := timeutils.FormatISO8601Duration(tc.duration)
			require.Equal(t, tc.expected, result)

			// Verify round-trip: format then parse should give original duration
			parsed, err := timeutils.ParseISO8601Duration(result)
			require.NoError(t, err)
			require.Equal(t, tc.duration, parsed)
		})
	}
}

//nolint:paralleltest // Test uses shared database state
func TestReleaseLease(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	// Create organization and worker
	org := models.NewOrganization("test-org", "")
	err := dbSvc.CreateOrganization(ctx, org)
	require.NoError(t, err)

	worker := models.NewWorker("test-worker", "Test Worker")
	_, err = dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	t.Run("BasicRelease", func(t *testing.T) {
		now := time.Now()
		scheduledAt := now.Add(-10 * time.Second)

		// Create check first (this automatically creates a check_job)
		check := models.NewCheck(org.UID, "test-check-"+uuid.New().String()[:8], "http")
		err := dbSvc.CreateCheck(ctx, check)
		require.NoError(t, err)

		// Retrieve the automatically created check_job
		checkJob := new(models.CheckJob)
		err = dbSvc.DB().NewSelect().
			Model(checkJob).
			Where("check_uid = ?", check.UID).
			Scan(ctx)
		require.NoError(t, err)

		// Update the check_job with test-specific values
		_, err = dbSvc.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("scheduled_at = ?", scheduledAt).
			Where("uid = ?", checkJob.UID).
			Exec(ctx)
		require.NoError(t, err)
		checkJob.ScheduledAt = &scheduledAt

		// Claim the job first by setting lease_worker_uid
		leaseExpiry := now.Add(60 * time.Second)
		_, err = dbSvc.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("lease_worker_uid = ?", worker.UID).
			Set("lease_expires_at = ?", leaseExpiry).
			Set("lease_starts = ?", 1).
			Where("uid = ?", checkJob.UID).
			Exec(ctx)
		require.NoError(t, err)

		// Update the local checkJob to reflect the lease
		checkJob.LeaseWorkerUID = &worker.UID
		checkJob.LeaseExpiresAt = &leaseExpiry
		checkJob.LeaseStarts = 1

		// Release lease
		err = runner.releaseLease(ctx, checkJob)
		require.NoError(t, err)

		// Verify the job was rescheduled
		updatedJob := new(models.CheckJob)
		err = dbSvc.DB().NewSelect().
			Model(updatedJob).
			Where("uid = ?", checkJob.UID).
			Scan(ctx)
		require.NoError(t, err)

		// Verify lease was released (worker UID should be nil)
		assert.Nil(t, updatedJob.LeaseWorkerUID)
	})
}

//nolint:paralleltest // Test uses shared database state
func TestExpressHandleEvent(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("test-worker", "Test Worker")
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	logger := runner.logger.With("test", "express")

	// Create a heartbeat check; it routes through executePassiveJob so the
	// express path can complete without making any outbound network calls.
	check := models.NewCheck(org.UID, "express-test-"+uuid.New().String()[:8], string(checkerdef.CheckTypeHeartbeat))
	check.Config = models.JSONMap{"token": "express-test-token"}
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	// CheckCreate already inserts a check_job; pull it out to confirm the express
	// path claims the right row.
	job := new(models.CheckJob)
	require.NoError(t, dbSvc.DB().NewSelect().Model(job).Where("check_uid = ?", check.UID).Scan(ctx))
	require.Nil(t, job.LeaseWorkerUID, "freshly-created job has no lease yet")

	t.Run("EmptyPayloadIsNoOp", func(t *testing.T) {
		runner.handleExpressEvent(ctx, logger, "{}")

		var refreshed models.CheckJob
		require.NoError(t, dbSvc.DB().NewSelect().Model(&refreshed).Where("uid = ?", job.UID).Scan(ctx))
		assert.Nil(t, refreshed.LeaseWorkerUID, "empty payload must not claim anything")
	})

	t.Run("UnknownCheckUIDIsNoOp", func(t *testing.T) {
		payload := `{"check_uid":"` + uuid.NewString() + `"}`
		runner.handleExpressEvent(ctx, logger, payload)

		var refreshed models.CheckJob
		require.NoError(t, dbSvc.DB().NewSelect().Model(&refreshed).Where("uid = ?", job.UID).Scan(ctx))
		assert.Nil(t, refreshed.LeaseWorkerUID, "unknown check_uid must not claim our job")
	})

	t.Run("ClaimsAndExecutesTargetedCheck", func(t *testing.T) {
		// Count existing real (non-Created) results before triggering.
		countResults := func() int {
			var n int
			err := dbSvc.DB().NewSelect().
				Model((*models.Result)(nil)).
				ColumnExpr("count(*)").
				Where("check_uid = ?", check.UID).
				Where("status != ?", int(models.ResultStatusCreated)).
				Scan(ctx, &n)
			require.NoError(t, err)
			return n
		}
		before := countResults()

		payload := `{"check_uid":"` + check.UID + `"}`
		runner.handleExpressEvent(ctx, logger, payload)

		assert.Greater(t, countResults(), before, "express path should produce at least one result row")

		// Lease should be released after executePassiveJob completes.
		var refreshed models.CheckJob
		require.NoError(t, dbSvc.DB().NewSelect().Model(&refreshed).Where("uid = ?", job.UID).Scan(ctx))
		assert.Nil(t, refreshed.LeaseWorkerUID, "lease should be released after execution")
		assert.Equal(t, 0, refreshed.LeaseStarts, "lease_starts reset after release")
	})
}

// TestSaveResultFallsBackToWorkerRegion covers the fix for spec
// 2026-07-07-04: a check job with no explicit region (checkJob.Region == nil,
// i.e. no regions[] configured on the check — the common single-worker/
// self-hosted case) must still get a non-nil Region on its saved result,
// falling back to the executing worker's own registered region rather than
// persisting NULL. Drives the real executeJob -> executePassiveJob ->
// saveResult path via a heartbeat check (no outbound network I/O), exactly
// like newHeartbeatJob elsewhere in this file.
//
//nolint:paralleltest // Test uses shared database state
func TestSaveResultFallsBackToWorkerRegion(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	testRegion := "eu-test-region"
	worker := models.NewWorker("test-worker", "Test Worker")
	worker.Region = &testRegion
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	check := models.NewCheck(org.UID, "region-fallback-"+uuid.New().String()[:8], string(checkerdef.CheckTypeHeartbeat))
	check.Config = models.JSONMap{"token": "region-fallback-token"}
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	checkJob := new(models.CheckJob)
	require.NoError(t, dbSvc.DB().NewSelect().Model(checkJob).Where("check_uid = ?", check.UID).Scan(ctx))
	require.Nil(t, checkJob.Region, "precondition: no explicit regions[] on the check means no region pinned on the job")
	claimJobForTest(t, dbSvc, ctx, checkJob, worker.UID)

	require.NoError(t, runner.executeJob(ctx, runner.logger, checkJob))

	result := new(models.Result)
	require.NoError(t, dbSvc.DB().NewSelect().
		Model(result).
		Where("check_uid = ?", check.UID).
		Where("status != ?", int(models.ResultStatusCreated)).
		Scan(ctx))
	require.NotNil(t, result.Region, "result.Region must fall back to the worker's own region instead of staying NULL")
	assert.Equal(t, testRegion, *result.Region)
}

// TestSaveErrorResultFallsBackToWorkerRegion is the saveErrorResult
// counterpart of TestSaveResultFallsBackToWorkerRegion: a job with no type
// set takes executeJob's generic-error branch straight to saveErrorResult
// (ErrNoCheckType), which must also fall back to the worker's own region.
//
//nolint:paralleltest // Test uses shared database state
func TestSaveErrorResultFallsBackToWorkerRegion(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	testRegion := "eu-test-region"
	worker := models.NewWorker("test-worker", "Test Worker")
	worker.Region = &testRegion
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	check := models.NewCheck(org.UID, "region-fallback-err-"+uuid.New().String()[:8], string(checkerdef.CheckTypeHTTP))
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	checkJob := new(models.CheckJob)
	require.NoError(t, dbSvc.DB().NewSelect().Model(checkJob).Where("check_uid = ?", check.UID).Scan(ctx))
	require.Nil(t, checkJob.Region, "precondition: no explicit regions[] on the check means no region pinned on the job")
	claimJobForTest(t, dbSvc, ctx, checkJob, worker.UID)
	checkJob.Type = "" // forces executeJob's ErrNoCheckType -> saveErrorResult branch

	// executeJob's return value here is saveErrorResult's own insertErr (the
	// DB write outcome), not ErrNoCheckType itself — that triggering error is
	// stored in the result's Output, so a nil return on success is expected.
	require.NoError(t, runner.executeJob(ctx, runner.logger, checkJob))

	result := new(models.Result)
	require.NoError(t, dbSvc.DB().NewSelect().
		Model(result).
		Where("check_uid = ?", check.UID).
		Where("status != ?", int(models.ResultStatusCreated)).
		Scan(ctx))
	require.NotNil(t, result.Status)
	assert.Equal(t, int(checkerdef.StatusError), *result.Status,
		"precondition: this must actually be the error-result branch")
	require.NotNil(t, result.Region,
		"error result.Region must fall back to the worker's own region instead of staying NULL")
	assert.Equal(t, testRegion, *result.Region)
}

// TestSaveResultKeepsExplicitJobRegion is the multi-region counterpart: when
// checkJob.Region is already pinned (as createCheckJobs does for checks with
// explicit regions[]), the fallback introduced for spec 2026-07-07-04 must
// NOT override it with the executing worker's own region, even when those
// differ — the job's assigned region always takes priority.
//
//nolint:paralleltest // Test uses shared database state
func TestSaveResultKeepsExplicitJobRegion(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	workerRegion := "eu-worker-region"
	worker := models.NewWorker("test-worker", "Test Worker")
	worker.Region = &workerRegion
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	check := models.NewCheck(org.UID, "region-pinned-"+uuid.New().String()[:8], string(checkerdef.CheckTypeHeartbeat))
	check.Config = models.JSONMap{"token": "region-pinned-token"}
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	checkJob := new(models.CheckJob)
	require.NoError(t, dbSvc.DB().NewSelect().Model(checkJob).Where("check_uid = ?", check.UID).Scan(ctx))
	claimJobForTest(t, dbSvc, ctx, checkJob, worker.UID)

	// Simulate the multi-region assignment createCheckJobs would have done
	// for a check with explicit regions[] — a region different from the
	// worker's own, so the two are clearly distinguishable in the assertion.
	assignedRegion := "us-assigned-region"
	checkJob.Region = &assignedRegion
	_, err = dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("region = ?", assignedRegion).
		Where("uid = ?", checkJob.UID).
		Exec(ctx)
	require.NoError(t, err)

	require.NoError(t, runner.executeJob(ctx, runner.logger, checkJob))

	result := new(models.Result)
	require.NoError(t, dbSvc.DB().NewSelect().
		Model(result).
		Where("check_uid = ?", check.UID).
		Where("status != ?", int(models.ResultStatusCreated)).
		Scan(ctx))
	require.NotNil(t, result.Region)
	assert.Equal(t, assignedRegion, *result.Region,
		"an explicitly assigned job region must win over the worker's own region")
}

//nolint:paralleltest // Test uses shared database state
func TestExecuteHeartbeatJob_RunningStatus(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	// Create organization and worker
	org := models.NewOrganization("test-org", "")
	err := dbSvc.CreateOrganization(ctx, org)
	require.NoError(t, err)

	worker := models.NewWorker("test-worker", "Test Worker")
	_, err = dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	// Create a heartbeat check
	check := models.NewCheck(org.UID, "test-heartbeat", "heartbeat")
	check.Config = models.JSONMap{"token": "test-token"}
	err = dbSvc.CreateCheck(ctx, check)
	require.NoError(t, err)

	// Get the check job
	checkJob := new(models.CheckJob)
	err = dbSvc.DB().NewSelect().
		Model(checkJob).
		Where("check_uid = ?", check.UID).
		Scan(ctx)
	require.NoError(t, err)

	// Set lease so we can release it
	leaseExpiry := time.Now().Add(60 * time.Second)
	_, err = dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("lease_worker_uid = ?", worker.UID).
		Set("lease_expires_at = ?", leaseExpiry).
		Set("lease_starts = ?", 1).
		Where("uid = ?", checkJob.UID).
		Exec(ctx)
	require.NoError(t, err)
	checkJob.LeaseWorkerUID = &worker.UID
	checkJob.LeaseExpiresAt = &leaseExpiry
	checkJob.LeaseStarts = 1

	t.Run("RunningWithinGracePeriod", func(t *testing.T) {
		// Insert a RUNNING result that is recent (within 2*period)
		resultUID, _ := uuid.NewV7()
		statusRunning := int(models.ResultStatusRunning)
		durationZero := float32(0)
		result := models.Result{
			UID:             resultUID.String(),
			OrganizationUID: org.UID,
			CheckUID:        check.UID,
			PeriodType:      "raw",
			PeriodStart:     time.Now().Add(-30 * time.Second), // Within 2*1m = 2m
			Status:          &statusRunning,
			Duration:        &durationZero,
			Metrics:         make(models.JSONMap),
			Output:          models.JSONMap{"message": "Run started"},
			CreatedAt:       time.Now(),
		}
		_, err := dbSvc.DB().NewInsert().Model(&result).Exec(ctx)
		require.NoError(t, err)

		// Re-set lease for another execution
		_, err = dbSvc.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("lease_worker_uid = ?", worker.UID).
			Set("lease_expires_at = ?", time.Now().Add(60*time.Second)).
			Set("lease_starts = ?", 1).
			Where("uid = ?", checkJob.UID).
			Exec(ctx)
		require.NoError(t, err)

		// Execute heartbeat job
		logger := runner.logger
		err = runner.executePassiveJob(ctx, logger, checkJob)
		require.NoError(t, err)

		// Get the latest result (should be RUNNING since within grace period)
		var results []*models.Result
		err = dbSvc.DB().NewSelect().
			Model(&results).
			Where("check_uid = ?", check.UID).
			Where("status = ?", int(models.ResultStatusRunning)).
			Order("created_at DESC").
			Limit(1).
			Scan(ctx)
		require.NoError(t, err)
		require.Len(t, results, 1)
	})

	t.Run("RunningExceedsGracePeriod", func(t *testing.T) {
		// Clear ALL previous results to avoid interference from previous sub-test
		_, err := dbSvc.DB().NewDelete().
			Model((*models.Result)(nil)).
			Where("check_uid = ?", check.UID).
			Exec(ctx)
		require.NoError(t, err)

		// Insert a RUNNING result that is old (exceeds 2*period)
		resultUID, _ := uuid.NewV7()
		statusRunning := int(models.ResultStatusRunning)
		durationZero := float32(0)
		result := models.Result{
			UID:             resultUID.String(),
			OrganizationUID: org.UID,
			CheckUID:        check.UID,
			PeriodType:      "raw",
			PeriodStart:     time.Now().Add(-5 * time.Minute), // Exceeds 2*1m = 2m
			Status:          &statusRunning,
			Duration:        &durationZero,
			Metrics:         make(models.JSONMap),
			Output:          models.JSONMap{"message": "Run started"},
			CreatedAt:       time.Now().Add(-5 * time.Minute),
		}
		_, err = dbSvc.DB().NewInsert().Model(&result).Exec(ctx)
		require.NoError(t, err)

		// Re-set lease for another execution
		_, err = dbSvc.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("lease_worker_uid = ?", worker.UID).
			Set("lease_expires_at = ?", time.Now().Add(60*time.Second)).
			Set("lease_starts = ?", 1).
			Where("uid = ?", checkJob.UID).
			Exec(ctx)
		require.NoError(t, err)

		// Execute heartbeat job
		logger := runner.logger
		err = runner.executePassiveJob(ctx, logger, checkJob)
		require.NoError(t, err)

		// Get the latest result (should be TIMEOUT since grace period exceeded)
		var results []*models.Result
		err = dbSvc.DB().NewSelect().
			Model(&results).
			Where("check_uid = ?", check.UID).
			Where("status = ?", int(models.ResultStatusTimeout)).
			Order("created_at DESC").
			Limit(1).
			Scan(ctx)
		require.NoError(t, err)
		require.Len(t, results, 1)

		// Verify the output message
		assert.Equal(t, "Run started but never completed", results[0].Output["message"])
	})
}

// TestLaneLimits covers the per-fetch reservation formula (spec 2026-07-01-03
// D3): slowBudget = max(0, (P − F) − busySlow), clamped to the free slots;
// fast always gets the full free capacity.
func TestLaneLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                       string
		free, pool, reserved, busy int
		wantFast, wantSlow         int
	}{
		{"idle pool, default floor", 4, 4, 1, 0, 4, 3},
		{"slow at the cap claims no more slow", 1, 4, 1, 3, 1, 0},
		{"partially busy slow", 2, 4, 1, 2, 2, 1},
		{"no reservation (F=0) lets slow fill the pool", 4, 4, 0, 0, 4, 4},
		{"slow budget clamped to free slots", 1, 25, 5, 0, 1, 1},
		{"busy beyond budget floors at zero", 2, 4, 1, 5, 2, 0},
		{"floor at pool−1 leaves one slow slot", 4, 4, 3, 0, 4, 1},
		{"no free slots", 0, 4, 1, 1, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fast, slow := laneLimits(tt.free, tt.pool, tt.reserved, tt.busy)
			require.Equal(t, tt.wantFast, fast, "fastLimit")
			require.Equal(t, tt.wantSlow, slow, "slowLimit")
		})
	}
}

// TestNewCheckWorkerClampsFastLaneReserved verifies the startup clamp (spec
// 2026-07-01-03 risk log): a floor at or above the pool size is pulled back to
// pool−1 so the slow lane is never silently killed; a negative floor becomes 0.
func TestNewCheckWorkerClampsFastLaneReserved(t *testing.T) {
	t.Parallel()

	newWorkerWith := func(poolSize, reserved int) *CheckWorker {
		cfg := &config.Config{
			Server: config.ServerConfig{
				CheckWorker: config.CheckWorkerConfig{Nb: poolSize, FetchMaxAhead: 5 * time.Minute},
				Scheduling:  config.SchedulingConfig{FastLaneReserved: reserved},
			},
		}

		return NewCheckWorker(nil, cfg, services.NewRegistry(), nil)
	}

	require.Equal(t, 3, newWorkerWith(4, 10).fastLaneReserved, "F >= P clamps to P−1")
	require.Equal(t, 3, newWorkerWith(4, 4).fastLaneReserved, "F == P clamps to P−1")
	require.Equal(t, 0, newWorkerWith(4, -2).fastLaneReserved, "negative F clamps to 0")
	require.Equal(t, 2, newWorkerWith(4, 2).fastLaneReserved, "in-range F is kept")
	require.Equal(t, 5, newWorkerWith(0, 5).fastLaneReserved, "default pool (25) keeps the default floor")
}

// TestFastLaneFloorInvariant is the core lane test (spec 2026-07-01-03): with
// pool P=4 and fast floor F=1, a saturating stream of slow sleep jobs may
// occupy at most P−F=3 runners (borrowing reaches exactly 3 while no fast work
// is due), and fast jobs added afterwards are claimed and complete while all 3
// slow probes are still in flight — the reserved slot is what serves them.
// Sleeps are ms-scale; assertions are ordering-based (fast results land before
// the first slow result) rather than wall-clock-based to stay robust on slow
// machines.
//
//nolint:paralleltest // Uses shared database state and a live worker; sequential phases
func TestFastLaneFloorInvariant(t *testing.T) {
	const (
		poolSize    = 4
		fastFloor   = 1
		slowSleepMs = 1200
		fastSleepMs = 20
	)

	ctx := context.Background()

	svc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	defer func() { _ = svc.Close() }()
	require.NoError(t, svc.Initialize(ctx))

	cfg := &config.Config{
		Server: config.ServerConfig{
			CheckWorker: config.CheckWorkerConfig{Nb: poolSize, FetchMaxAhead: 5 * time.Minute},
			Scheduling: config.SchedulingConfig{
				FastLaneReserved:    fastFloor,
				LaneSlowThresholdMs: 2000,
				LaneFastThresholdMs: 1000,
			},
		},
	}

	svcList := services.NewRegistry()
	checkJobSvc := checkjobsvc.NewService(svc.DB())
	svcList.CheckJobs = checkJobSvc
	eventNotifier := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = eventNotifier.Close() })
	svcList.EventNotifier = eventNotifier

	runner := NewCheckWorker(svc, cfg, svcList, checkJobSvc)

	org := models.NewOrganization("lane-org", "")
	require.NoError(t, svc.CreateOrganization(ctx, org))

	// createSleepJob creates an enabled sleep check and stamps its job with the
	// wanted lane + a matching cost EWMA, due 1s ago (no pre-exec sleep).
	// Returns the check UID.
	createSleepJob := func(slug string, sleepMs int, lane uint8, costEWMAMs float64) string {
		check := models.NewCheck(org.UID, slug, string(checkerdef.CheckTypeSleep))
		check.Config = models.JSONMap{"sleep_ms": sleepMs}
		require.NoError(t, svc.CreateCheck(ctx, check))

		due := time.Now().Add(-time.Second)
		res, upErr := svc.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("lane = ?", lane).
			Set("cost_ewma_ms = ?", costEWMAMs).
			Set("scheduled_at = ?", due).
			Set("effective_scheduled_at = ?", due).
			Where("check_uid = ?", check.UID).
			Exec(ctx)
		require.NoError(t, upErr)
		n, upErr := res.RowsAffected()
		require.NoError(t, upErr)
		require.EqualValues(t, 1, n, "sleep check must have exactly one job")

		return check.UID
	}

	// realResultCount counts non-"created" results for a check.
	realResultCount := func(checkUID string) int {
		var n int
		require.NoError(t, svc.DB().NewSelect().
			Model((*models.Result)(nil)).
			ColumnExpr("count(*)").
			Where("check_uid = ?", checkUID).
			Where("status != ?", int(models.ResultStatusCreated)).
			Scan(ctx, &n))

		return n
	}

	// waitFor polls cond, re-nudging the fetcher via a payload-less
	// check.created event each round (the 1-buffered notifier channel may have
	// dropped an earlier nudge).
	waitFor := func(timeout time.Duration, msg string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			_ = eventNotifier.Notify(ctx, "check.created", "{}")
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timed out: %s", msg)
	}

	// Sample busySlow continuously: the floor invariant must hold at every
	// instant, not just at the poll points.
	var maxBusySlow atomic.Int32
	samplerCtx, samplerCancel := context.WithCancel(ctx)
	samplerDone := make(chan struct{})
	go func() {
		defer close(samplerDone)
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-samplerCtx.Done():
				return
			case <-ticker.C:
				if b := runner.busySlow.Load(); b > maxBusySlow.Load() {
					maxBusySlow.Store(b)
				}
			}
		}
	}()

	runCtx, cancel := context.WithCancel(ctx)
	runDone := make(chan error, 1)
	go func() { runDone <- runner.Run(runCtx) }()

	waitFor(3*time.Second, "worker registration", func() bool { return runner.getWorker() != nil })
	waitFor(3*time.Second, "runner pool idle", func() bool {
		return runner.availableRunners.Load() == poolSize
	})

	// Phase 1 — saturate with slow work only: borrowing must reach exactly
	// P−F in-flight slow probes (an idle fast stream donates its slots), and
	// never more.
	slowUIDs := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		slowUIDs = append(slowUIDs, createSleepJob(
			fmt.Sprintf("lane-slow-%d", i), slowSleepMs, scheduling.LaneSlow, 3000,
		))
	}

	waitFor(5*time.Second, "slow lane saturation (busySlow == P−F)", func() bool {
		return runner.busySlow.Load() == poolSize-fastFloor
	})

	// Phase 2 — with all 3 slow slots occupied, add due fast jobs: the
	// reserved slot must serve them while the slow probes are still asleep.
	fastUIDs := []string{
		createSleepJob("lane-fast-0", fastSleepMs, scheduling.LaneFast, 0),
		createSleepJob("lane-fast-1", fastSleepMs, scheduling.LaneFast, 0),
	}

	waitFor(5*time.Second, "fast checks complete on the reserved slot", func() bool {
		for _, uid := range fastUIDs {
			if realResultCount(uid) == 0 {
				return false
			}
		}

		return true
	})

	// The fast results must have landed while the slow probes were still in
	// flight: none of the slow checks may have a real result yet, and the
	// slow lane must still be at its cap.
	for _, uid := range slowUIDs {
		require.Zero(t, realResultCount(uid),
			"fast checks must complete before any slow probe finishes — the reserved slot served them")
	}
	require.EqualValues(t, poolSize-fastFloor, runner.busySlow.Load(),
		"all P−F slow probes are still in flight when the fast results land")

	// Shutdown; let in-flight sleeps drain.
	cancel()
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not shut down")
	}
	samplerCancel()
	<-samplerDone

	require.LessOrEqual(t, maxBusySlow.Load(), int32(poolSize-fastFloor),
		"in-flight slow probes must never exceed P − F")
	require.EqualValues(t, poolSize-fastFloor, maxBusySlow.Load(),
		"borrowing must reach exactly P − F while no fast work is due")
}

//nolint:paralleltest // Test uses shared database state
func TestGracefulShutdown(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	// Create organization and worker
	org := models.NewOrganization("test-org", "")
	err := dbSvc.CreateOrganization(ctx, org)
	require.NoError(t, err)

	// Create a context that we can cancel to trigger shutdown
	shutdownCtx, cancel := context.WithCancel(ctx)

	// Start the check worker in a goroutine
	runDone := make(chan error, 1)
	go func() {
		runDone <- runner.Run(shutdownCtx)
	}()

	// Give the worker some time to start up (register worker, start heartbeat, start worker goroutines)
	time.Sleep(200 * time.Millisecond)

	// Verify worker was registered
	require.NotNil(t, runner.getWorker(), "worker should be registered")

	// Trigger graceful shutdown
	cancel()

	// Wait for the Run() method to complete with a timeout
	// This ensures that:
	// 1. All worker goroutines finish (tracked by WaitGroup)
	// 2. The heartbeat goroutine finishes (should be tracked by WaitGroup)
	select {
	case runErr := <-runDone:
		// Run should return context.Canceled error
		require.ErrorIs(t, runErr, context.Canceled, "Run should return context.Canceled error on graceful shutdown")
	case <-time.After(3 * time.Second):
		t.Fatal("graceful shutdown timed out - WaitGroup may not be tracking all goroutines properly")
	}

	// Verify the worker was properly updated in the database before shutdown
	var dbWorker models.Worker
	err = dbSvc.DB().NewSelect().
		Model(&dbWorker).
		Where("uid = ?", runner.getWorker().UID).
		Scan(ctx)
	require.NoError(t, err)
	require.NotNil(t, dbWorker.LastActiveAt, "worker should have last_active_at timestamp")
}

// --- Watchdog tests (spec 2026-07-05-05) ---
//
// The checker registry (registry.GetChecker) is a hardcoded switch keyed on
// check type, so there is no seam to inject a stub checker through it. The
// spec's own runCheckerGuarded(ctx, checker, cfg, execTimeout) signature
// already takes a checkerdef.Checker value directly, so these tests build
// hand-rolled stub checkers and pass them straight into runCheckerGuarded —
// no registry changes needed. The well-behaved (ctx-honoring) case reuses the
// real, registered checksleep.SleepChecker.

// metricDelta is the tolerance used with assert/require.InDelta when
// comparing prommetrics counter/gauge float64 values (testutil.ToFloat64):
// these are always integer-valued in these tests, so a tiny epsilon is
// exact-equivalent while satisfying the testifylint float-compare rule.
const metricDelta = 0.001

// hangingConfig is a no-op checkerdef.Config for the stub checkers below —
// they don't read any config fields.
type hangingConfig struct{}

func (hangingConfig) FromMap(map[string]any) error { return nil }
func (hangingConfig) GetConfig() map[string]any    { return map[string]any{} }

// timeoutRecorder captures the `timeout` value a checker config observed via
// FromMap. FromMap runs synchronously inside executeJob (on the calling
// goroutine, before runCheckerGuarded spawns the child), so the test reads
// these fields after executeJob returns without any synchronization.
type timeoutRecorder struct {
	seen  bool
	value string
}

// timeoutRecordingConfig records the `timeout` value FromMap received so a test
// can assert executeJob threads the uniform default into the checker config
// when the user left `timeout` unset (spec 2026-07-11-09).
type timeoutRecordingConfig struct{ rec *timeoutRecorder }

func (c timeoutRecordingConfig) FromMap(configMap map[string]any) error {
	if raw, ok := configMap[checkTimeoutConfigKey].(string); ok {
		c.rec.seen = true
		c.rec.value = raw
	}

	return nil
}
func (c timeoutRecordingConfig) GetConfig() map[string]any { return map[string]any{} }

// hangingChecker's Execute blocks forever on a never-closed channel and
// completely ignores ctx — the exact failure mode from the 2026-07-04/05
// incident (mail checkers with unbounded socket reads).
type hangingChecker struct {
	// unblock, when non-nil, is closed by the test to let a previously
	// abandoned Execute call finally return (proves D4: late finish is
	// discarded).
	unblock chan struct{}
	started chan struct{} // closed once Execute has actually started running
}

func (c *hangingChecker) Type() checkerdef.CheckType           { return checkerdef.CheckType("test-hanging") }
func (c *hangingChecker) Validate(*checkerdef.CheckSpec) error { return nil }

func (c *hangingChecker) Execute(_ context.Context, _ checkerdef.Config) (*checkerdef.Result, error) {
	if c.started != nil {
		close(c.started)
	}

	<-c.unblock // block until the test says "go" — ignores ctx entirely, exactly like the incident

	return &checkerdef.Result{Status: checkerdef.StatusUp}, nil
}

// newHangingChecker returns a hangingChecker whose Execute blocks until the
// test unblocks it. The unblock channel is always closed via t.Cleanup, so
// the child goroutine spawned by runCheckerGuarded eventually exits even
// though the test itself proceeds as soon as the watchdog abandons it —
// otherwise the goroutine would leak for the lifetime of the whole test
// binary and trip the package's goleak.VerifyTestMain (leak_test.go).
//
// The cleanup also waits for the unblocked child to actually exit: its
// deferred DecCheckRunnerAbandonedActive fires whenever the scheduler next
// runs the goroutine, and letting the test end before that leaks the
// decrement into the following test's before/after snapshots of the
// package-global gauge (their exact-delta assertions then fail — verified by
// injecting a 120ms delay after <-unblock, which reliably broke the two
// tests that run after newHangingChecker users). Draining here keeps the
// gauge quiescent at test boundaries.
func newHangingChecker(t *testing.T) *hangingChecker {
	t.Helper()

	gaugeBaseline := testutil.ToFloat64(prommetrics.CheckRunnerAbandonedActive)
	unblock := make(chan struct{})
	t.Cleanup(func() {
		close(unblock)
		deadline := time.Now().Add(5 * time.Second)
		for testutil.ToFloat64(prommetrics.CheckRunnerAbandonedActive) > gaugeBaseline {
			if time.Now().After(deadline) {
				t.Errorf("abandoned checker goroutine did not exit within 5s; CheckRunnerAbandonedActive still %v (baseline %v)",
					testutil.ToFloat64(prommetrics.CheckRunnerAbandonedActive), gaugeBaseline)

				return
			}
			time.Sleep(time.Millisecond)
		}
	})

	return &hangingChecker{unblock: unblock}
}

// panickingChecker's Execute panics immediately (spec D2).
type panickingChecker struct{}

func (panickingChecker) Type() checkerdef.CheckType           { return checkerdef.CheckType("test-panicking") }
func (panickingChecker) Validate(*checkerdef.CheckSpec) error { return nil }

func (panickingChecker) Execute(context.Context, checkerdef.Config) (*checkerdef.Result, error) {
	panic("simulated checker panic")
}

// deadlineRecordingChecker records the context deadline its Execute observes,
// then returns immediately with StatusUp. Used to assert executeJob builds the
// execution context with a checkTimeout + 1s deadline (spec 2026-07-10-11). The
// channel send/receive inside runCheckerGuarded → executeJob establishes
// happens-before with the test's read, but atomics keep the race detector
// unambiguously happy regardless.
type deadlineRecordingChecker struct {
	deadlineUnixNano atomic.Int64
	observed         atomic.Bool
}

func (c *deadlineRecordingChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckType("test-deadline")
}
func (c *deadlineRecordingChecker) Validate(*checkerdef.CheckSpec) error { return nil }

func (c *deadlineRecordingChecker) Execute(ctx context.Context, _ checkerdef.Config) (*checkerdef.Result, error) {
	if dl, ok := ctx.Deadline(); ok {
		c.deadlineUnixNano.Store(dl.UnixNano())
		c.observed.Store(true)
	}

	return &checkerdef.Result{Status: checkerdef.StatusUp}, nil
}

// withShortAbandonGrace overrides abandonGrace for the duration of the test
// so hang/panic watchdog tests don't have to wait out the real 5s grace.
func withShortAbandonGrace(t *testing.T, grace time.Duration) {
	t.Helper()
	abandonGraceOverride = grace
	t.Cleanup(func() { abandonGraceOverride = 0 })
}

func testCheckJob(checkType string) *models.CheckJob {
	return &models.CheckJob{
		UID:             uuid.New().String(),
		OrganizationUID: "test-org",
		CheckUID:        uuid.New().String(),
		Type:            checkType,
	}
}

//nolint:paralleltest // mutates the package-level abandonGraceOverride
func TestRunCheckerGuarded_HangingChecker_Abandoned(t *testing.T) {
	withShortAbandonGrace(t, 50*time.Millisecond)

	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	region := "eu2"
	checkJob := testCheckJob("test-hanging")
	checkJob.Region = &region

	before := testutil.ToFloat64(prommetrics.CheckRunnerAbandoned.WithLabelValues(checkJob.Type))
	gaugeBefore := testutil.ToFloat64(prommetrics.CheckRunnerAbandonedActive)

	execTimeout := 20 * time.Millisecond
	start := time.Now()
	result, err := runner.runCheckerGuarded(
		ctx, runner.logger, newHangingChecker(t), hangingConfig{}, checkJob, execTimeout, start,
	)
	elapsed := time.Since(start)

	require.NoError(t, err, "abandonment returns a ready-made result, not an error")
	require.NotNil(t, result)
	assert.Equal(t, checkerdef.StatusTimeout, result.Status)
	assert.Contains(t, result.Output[checkerdef.OutputKeyError], "abandoned")

	// Returned within execTimeout + grace + a small epsilon — proves the
	// watchdog actually fired instead of hanging forever.
	assert.Less(t, elapsed, execTimeout+50*time.Millisecond+500*time.Millisecond)

	after := testutil.ToFloat64(prommetrics.CheckRunnerAbandoned.WithLabelValues(checkJob.Type))
	assert.InDelta(t, before+1, after, metricDelta, "abandoned counter must increment")

	gaugeAfter := testutil.ToFloat64(prommetrics.CheckRunnerAbandonedActive)
	assert.InDelta(t, gaugeBefore+1, gaugeAfter, metricDelta,
		"abandoned-active gauge must increment (checker never finished)")
}

//nolint:paralleltest // mutates the package-level abandonGraceOverride
func TestRunCheckerGuarded_LateFinish_DiscardedAndGaugeDecrements(t *testing.T) {
	withShortAbandonGrace(t, 50*time.Millisecond)

	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	checkJob := testCheckJob("test-hanging-late")
	unblock := make(chan struct{})
	started := make(chan struct{})
	checker := &hangingChecker{unblock: unblock, started: started}

	gaugeBefore := testutil.ToFloat64(prommetrics.CheckRunnerAbandonedActive)

	execTimeout := 20 * time.Millisecond
	result, err := runner.runCheckerGuarded(
		ctx, runner.logger, checker, hangingConfig{}, checkJob, execTimeout, time.Now(),
	)
	require.NoError(t, err)
	require.Equal(t, checkerdef.StatusTimeout, result.Status, "watchdog result wins (D4)")

	gaugeDuringAbandon := testutil.ToFloat64(prommetrics.CheckRunnerAbandonedActive)
	assert.InDelta(t, gaugeBefore+1, gaugeDuringAbandon, metricDelta,
		"gauge incremented while the child is still outstanding")

	// Now let the previously-abandoned child finally finish. Its send lands
	// in the cap-1 buffer that nobody reads (D4) — no double result is
	// produced by runCheckerGuarded itself (it already returned above), and
	// the deferred cleanup must decrement the gauge back down.
	close(unblock)

	require.Eventually(t, func() bool {
		return testutil.ToFloat64(prommetrics.CheckRunnerAbandonedActive) == gaugeBefore
	}, 2*time.Second, 10*time.Millisecond, "gauge must decrement once the late child actually returns")
}

//nolint:paralleltest // mutates the package-level abandonGraceOverride
func TestRunCheckerGuarded_PanickingChecker_ReturnsErrorNotCrash(t *testing.T) {
	withShortAbandonGrace(t, 5*time.Second) // grace must not matter: panic returns immediately

	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	checkJob := testCheckJob("test-panicking")

	start := time.Now()
	result, err := runner.runCheckerGuarded(
		ctx, runner.logger, panickingChecker{}, hangingConfig{}, checkJob, time.Second, start,
	)
	elapsed := time.Since(start)

	// The mere fact this line runs (in the same test binary) proves the
	// panic did not crash the process — recover() contained it (D2).
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCheckerPanic)
	assert.Contains(t, err.Error(), "simulated checker panic")
	assert.Nil(t, result, "panic path returns (nil, error); executeJob's generic-error branch builds the result")
	assert.Less(t, elapsed, time.Second, "panic must be reported immediately, not after the grace window")
}

//nolint:paralleltest // mutates the package-level abandonGraceOverride
func TestRunCheckerGuarded_WellBehavedCtxHonoringChecker_WatchdogDoesNotFire(t *testing.T) {
	withShortAbandonGrace(t, 200*time.Millisecond)

	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	checkJob := testCheckJob(string(checkerdef.CheckTypeSleep))

	// checksleep.SleepChecker already honors ctx (proven independently by
	// TestSleepChecker_Execute_ContextTimeout in the checksleep package): a
	// context deadline shorter than sleep_ms interrupts the sleep and
	// returns its own StatusTimeout well before the watchdog's grace window.
	checker, ok := registry.GetChecker(checkerdef.CheckTypeSleep)
	require.True(t, ok, "checksleep.SleepChecker must be registered")

	cfg, ok := registry.ParseConfig(checkerdef.CheckTypeSleep)
	require.True(t, ok)
	require.NoError(t, cfg.FromMap(map[string]any{"sleep_ms": 5000, "jitter_ms": 0}))

	gaugeBefore := testutil.ToFloat64(prommetrics.CheckRunnerAbandonedActive)
	abandonedBefore := testutil.ToFloat64(prommetrics.CheckRunnerAbandoned.WithLabelValues(checkJob.Type))

	// execCtx must actually carry a deadline for the checker to observe —
	// runCheckerGuarded passes ctx straight through to checker.Execute.
	execTimeout := 30 * time.Millisecond
	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	start := time.Now()
	result, err := runner.runCheckerGuarded(execCtx, runner.logger, checker, cfg, checkJob, execTimeout, start)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, checkerdef.StatusTimeout, result.Status, "the checker's own ctx-driven timeout, not the watchdog's")
	// Returns promptly on the checker's own deadline — well under the 200ms
	// grace — proving the watchdog's 5s/grace path never engaged.
	assert.Less(t, elapsed, 200*time.Millisecond)

	gaugeAfter := testutil.ToFloat64(prommetrics.CheckRunnerAbandonedActive)
	assert.InDelta(t, gaugeBefore, gaugeAfter, metricDelta, "watchdog must not fire for a ctx-honoring checker")

	abandonedAfter := testutil.ToFloat64(prommetrics.CheckRunnerAbandoned.WithLabelValues(checkJob.Type))
	assert.InDelta(t, abandonedBefore, abandonedAfter, metricDelta, "abandoned counter must not move")
}

// TestExecuteJob_ExecutionContextIsCheckTimeoutPlusOneSecond drives the real
// executeJob path and asserts the execution context handed to the checker has a
// deadline of checkTimeout + 1s (spec 2026-07-10-11). The +1s margin lets a
// checker that honors its own timeout report a clean StatusTimeout before the
// hard context cancellation; without it, the context and the checker's internal
// timeout race and the result is a generic context-deadline-exceeded.
//
//nolint:paralleltest // Test uses shared database state
func TestExecuteJob_ExecutionContextIsCheckTimeoutPlusOneSecond(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("test-worker", "Test Worker")
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	const dlType = checkerdef.CheckType("test-deadline")
	recorder := &deadlineRecordingChecker{}
	runner.getChecker, runner.parseConfig = stubResolvers(dlType, recorder)

	// Flat 3s check timeout (cost-aware off, CostTimeoutFactor 0), so
	// executeJob derives checkTimeout = 3s deterministically regardless of the
	// job's cost EWMA and the execution context deadline is start + 4s.
	const checkTimeout = 3 * time.Second
	runner.schedParams = scheduling.Params{CheckTimeout: checkTimeout}

	check := models.NewCheck(org.UID, "deadline-"+uuid.New().String()[:8], string(dlType))
	require.NoError(t, dbSvc.CreateCheck(ctx, check))
	checkJob := new(models.CheckJob)
	require.NoError(t, dbSvc.DB().NewSelect().Model(checkJob).Where("check_uid = ?", check.UID).Scan(ctx))
	claimJobForTest(t, dbSvc, ctx, checkJob, worker.UID)

	start := time.Now()
	require.NoError(t, runner.executeJob(ctx, runner.logger, checkJob))

	require.True(t, recorder.observed.Load(), "the checker must observe a context deadline")
	deadline := time.Unix(0, recorder.deadlineUnixNano.Load())
	observed := deadline.Sub(start)

	assert.InDelta(t, (checkTimeout + time.Second).Seconds(), observed.Seconds(), 0.5,
		"execution context deadline must be checkTimeout + 1s")
	assert.Greater(t, observed, checkTimeout,
		"the +1s margin must be present so a ctx-honoring checker can report StatusTimeout before the hard cancel")
}

// TestExecuteJob_UnsetTimeoutThreadsUniformDefault proves that when a check has
// no per-check `timeout`, executeJob threads the uniform 15s default into the
// checker config (spec 2026-07-11-09) so every type honors it instead of its
// own short internal defaultTimeout (e.g. icmp's 5s), and the execution context
// deadline is 15s + 1s. This is the observable "unset = 15s" contract: the
// existing deadline test proves the context is checkTimeout + 1s, but only this
// one proves the checker itself is handed the 15s budget rather than its short
// default.
//
//nolint:paralleltest // Test uses shared database state
func TestExecuteJob_UnsetTimeoutThreadsUniformDefault(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("test-worker", "Test Worker")
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	const dlType = checkerdef.CheckType("test-deadline")
	recorder := &deadlineRecordingChecker{}
	rec := &timeoutRecorder{}
	runner.getChecker = func(ct checkerdef.CheckType) (checkerdef.Checker, bool) {
		if ct == dlType {
			return recorder, true
		}

		return registry.GetChecker(ct)
	}
	runner.parseConfig = func(ct checkerdef.CheckType) (checkerdef.Config, bool) {
		if ct == dlType {
			return timeoutRecordingConfig{rec: rec}, true
		}

		return registry.ParseConfig(ct)
	}

	// Uniform 15s default (cost-aware off, CostTimeoutFactor 0), so an unset
	// timeout resolves to exactly 15s regardless of the job's cost EWMA and the
	// execution context deadline is start + 16s.
	const checkTimeout = 15 * time.Second
	runner.schedParams = scheduling.Params{CheckTimeout: checkTimeout}

	// Check config carries no `timeout` key.
	check := models.NewCheck(org.UID, "unset-"+uuid.New().String()[:8], string(dlType))
	require.NoError(t, dbSvc.CreateCheck(ctx, check))
	checkJob := new(models.CheckJob)
	require.NoError(t, dbSvc.DB().NewSelect().Model(checkJob).Where("check_uid = ?", check.UID).Scan(ctx))
	claimJobForTest(t, dbSvc, ctx, checkJob, worker.UID)

	start := time.Now()
	require.NoError(t, runner.executeJob(ctx, runner.logger, checkJob))

	require.True(t, rec.seen, "the checker config must observe a threaded timeout when the user left it unset")
	require.Equal(t, checkTimeout.String(), rec.value,
		"unset timeout must be threaded to the checker as the uniform 15s default, not left to its short internal default")

	require.True(t, recorder.observed.Load(), "the checker must observe a context deadline")
	deadline := time.Unix(0, recorder.deadlineUnixNano.Load())
	observed := deadline.Sub(start)
	assert.InDelta(t, (checkTimeout + time.Second).Seconds(), observed.Seconds(), 0.5,
		"execution context deadline must be 15s + 1s for an unset timeout")
}

// TestPerCheckTimeout pins the worker-side extraction of the per-check
// `timeout` config value (spec 2026-07-11-05): duration strings in (0, 30s]
// are honored, legacy over-cap values are clamped at 30s, and anything
// absent/invalid/non-positive falls back to the global rule.
func TestPerCheckTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config map[string]any
		want   time.Duration
		wantOK bool
	}{
		{name: "absent key", config: map[string]any{"url": "https://example.com"}, wantOK: false},
		{name: "nil config", config: nil, wantOK: false},
		{name: "valid 10s", config: map[string]any{"timeout": "10s"}, want: 10 * time.Second, wantOK: true},
		{name: "sub-second", config: map[string]any{"timeout": "500ms"}, want: 500 * time.Millisecond, wantOK: true},
		{name: "30s boundary", config: map[string]any{"timeout": "30s"}, want: 30 * time.Second, wantOK: true},
		{name: "legacy 60s clamps to 30s", config: map[string]any{"timeout": "60s"}, want: 30 * time.Second, wantOK: true},
		{name: "zero ignored", config: map[string]any{"timeout": "0s"}, wantOK: false},
		{name: "negative ignored", config: map[string]any{"timeout": "-5s"}, wantOK: false},
		{name: "garbage ignored", config: map[string]any{"timeout": "abc"}, wantOK: false},
		{name: "empty string ignored", config: map[string]any{"timeout": ""}, wantOK: false},
		{name: "non-string ignored", config: map[string]any{"timeout": float64(10)}, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := perCheckTimeout(tt.config)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestExecuteJob_PerCheckTimeoutSetsContextDeadline drives the real executeJob
// path with a per-check `timeout` config value (spec 2026-07-11-05) and
// asserts the execution context deadline is that timeout + 1s — taking
// precedence over the flat/cost-aware ceiling in both directions (shorter and
// longer than the global budget) and defensively clamped at 30s for legacy
// over-cap configs.
//
//nolint:paralleltest // Test uses shared database state
func TestExecuteJob_PerCheckTimeoutSetsContextDeadline(t *testing.T) {
	// Flat 3s global check timeout (cost-aware off): without a per-check
	// timeout the context deadline would be 4s — every case below must
	// deviate from that per the per-check rule.
	const globalCheckTimeout = 3 * time.Second

	tests := []struct {
		name         string
		timeout      string
		wantDeadline time.Duration
	}{
		{name: "shorter than the global budget", timeout: "2s", wantDeadline: 3 * time.Second},
		{name: "longer than the global budget (user choice wins)", timeout: "8s", wantDeadline: 9 * time.Second},
		{name: "legacy 60s clamps to 30s (+1s context)", timeout: "60s", wantDeadline: 31 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { //nolint:paralleltest // shared database state
			runner, dbSvc, ctx := setupTestRunner(t)
			defer func() { _ = dbSvc.Close() }()

			org := models.NewOrganization("test-org", "")
			require.NoError(t, dbSvc.CreateOrganization(ctx, org))

			worker := models.NewWorker("test-worker", "Test Worker")
			_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
			require.NoError(t, err)
			runner.setWorker(worker)

			const dlType = checkerdef.CheckType("test-deadline")
			recorder := &deadlineRecordingChecker{}
			runner.getChecker, runner.parseConfig = stubResolvers(dlType, recorder)
			runner.schedParams = scheduling.Params{CheckTimeout: globalCheckTimeout}

			check := models.NewCheck(org.UID, "percheck-"+uuid.New().String()[:8], string(dlType))
			check.Config = models.JSONMap{"timeout": tt.timeout}
			require.NoError(t, dbSvc.CreateCheck(ctx, check))
			checkJob := new(models.CheckJob)
			require.NoError(t, dbSvc.DB().NewSelect().Model(checkJob).Where("check_uid = ?", check.UID).Scan(ctx))
			claimJobForTest(t, dbSvc, ctx, checkJob, worker.UID)

			start := time.Now()
			require.NoError(t, runner.executeJob(ctx, runner.logger, checkJob))

			require.True(t, recorder.observed.Load(), "the checker must observe a context deadline")
			deadline := time.Unix(0, recorder.deadlineUnixNano.Load())
			observed := deadline.Sub(start)

			assert.InDelta(t, tt.wantDeadline.Seconds(), observed.Seconds(), 0.5,
				"execution context deadline must be the per-check timeout + 1s")
		})
	}
}

// stubResolvers builds getChecker/parseConfig overrides that resolve exactly
// one synthetic check type to the given stub checker, and fall through to
// the real registry for everything else (so a subsequent heartbeat/sleep job
// on the same runner still resolves normally). This is the seam
// (CheckWorker.getChecker/parseConfig) that lets the integration tests below
// drive the real jobsChan -> runnerLoop -> executeJob path — the same path
// production traffic takes — with a checker that deliberately hangs or
// panics, which the hardcoded registry switch has no way to express.
func stubResolvers(checkType checkerdef.CheckType, checker checkerdef.Checker) (
	func(checkerdef.CheckType) (checkerdef.Checker, bool),
	func(checkerdef.CheckType) (checkerdef.Config, bool),
) {
	getChecker := func(ct checkerdef.CheckType) (checkerdef.Checker, bool) {
		if ct == checkType {
			return checker, true
		}

		return registry.GetChecker(ct)
	}
	parseConfig := func(ct checkerdef.CheckType) (checkerdef.Config, bool) {
		if ct == checkType {
			return hangingConfig{}, true
		}

		return registry.ParseConfig(ct)
	}

	return getChecker, parseConfig
}

// startTestRunnerLoop starts one real runnerLoop goroutine against a fresh
// jobsChan, isolated from the rest of the worker (Run() is not started), so
// tests drive exactly the pool machinery under test (availableRunners,
// jobsChan, executeJob) without unrelated fetcher/express activity. Returns
// a cleanup that cancels the loop and waits for it to exit.
// claimJobForTest stamps the given job as leased by workerUID, both in the DB
// (so releaseLease's `WHERE lease_worker_uid = ?` matches, exactly like a
// real ClaimJobs would have left it) and on the in-memory job struct handed
// to jobsChan. Mirrors the lease-setup already used by
// TestExecuteHeartbeatJob_RunningStatus elsewhere in this file.
func claimJobForTest(
	t *testing.T, dbSvc *sqlite.Service, ctx context.Context, job *models.CheckJob, workerUID string, //nolint:revive
) {
	t.Helper()

	leaseExpiry := time.Now().Add(60 * time.Second)
	_, err := dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("lease_worker_uid = ?", workerUID).
		Set("lease_expires_at = ?", leaseExpiry).
		Set("lease_starts = ?", 1).
		Where("uid = ?", job.UID).
		Exec(ctx)
	require.NoError(t, err)

	job.LeaseWorkerUID = &workerUID
	job.LeaseExpiresAt = &leaseExpiry
	job.LeaseStarts = 1
}

func startTestRunnerLoop(
	t *testing.T, runner *CheckWorker, ctx context.Context, //nolint:revive
) {
	t.Helper()

	runner.jobsChan = make(chan *models.CheckJob)
	runnerDone := make(chan struct{})
	runner.wg.Add(1)

	loopCtx, cancelLoop := context.WithCancel(ctx)

	go func() {
		defer close(runnerDone)
		runner.runnerLoop(loopCtx, 0)
	}()

	t.Cleanup(func() {
		cancelLoop()
		select {
		case <-runnerDone:
		case <-time.After(2 * time.Second):
			t.Fatal("runnerLoop did not exit after context cancellation")
		}
	})
}

// sendJobAndWaitForResult pushes a job onto the runner's jobsChan (as the
// fetcher normally would) and waits for a non-"created" result row to appear
// for its check — i.e. for the *same* runnerLoop goroutine to pick it up,
// execute it, and complete. Used to prove the runner survived a prior
// abandonment/panic: a lost goroutine would never drain jobsChan again and
// this would time out.
func sendJobAndWaitForResult(
	t *testing.T, dbSvc *sqlite.Service,
	ctx context.Context, //nolint:revive
	jobsChan chan *models.CheckJob, job *models.CheckJob,
) {
	t.Helper()

	select {
	case jobsChan <- job:
	case <-time.After(2 * time.Second):
		t.Fatal("runnerLoop did not accept a subsequent job — the goroutine was lost")
	}

	require.Eventually(t, func() bool {
		var n int
		countErr := dbSvc.DB().NewSelect().
			Model((*models.Result)(nil)).
			ColumnExpr("count(*)").
			Where("check_uid = ?", job.CheckUID).
			Where("status != ?", int(models.ResultStatusCreated)).
			Scan(ctx, &n)
		require.NoError(t, countErr)

		return n > 0
	}, 3*time.Second, 20*time.Millisecond, "runnerLoop must process the subsequent job — proving it survived")
}

// newHeartbeatJob creates a real heartbeat check (routes through
// executePassiveJob, no outbound I/O), claims its job for workerUID (so
// releaseLease succeeds exactly as it would for a normally-claimed job), and
// returns the job row — used as the innocuous "subsequent job" that proves
// the runner survived.
func newHeartbeatJob(
	t *testing.T, dbSvc *sqlite.Service, ctx context.Context, orgUID, workerUID string, //nolint:revive
) *models.CheckJob {
	t.Helper()

	check := models.NewCheck(orgUID, "watchdog-next-"+uuid.New().String()[:8], string(checkerdef.CheckTypeHeartbeat))
	check.Config = models.JSONMap{"token": "watchdog-next-token"}
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	job := new(models.CheckJob)
	require.NoError(t, dbSvc.DB().NewSelect().Model(job).Where("check_uid = ?", check.UID).Scan(ctx))
	claimJobForTest(t, dbSvc, ctx, job, workerUID)

	return job
}

// fastExecTimeoutCostEWMAMs is the seeded cost EWMA (ms) that, combined with
// useFastExecTimeout's schedParams, makes executeJob compute a ~20ms
// execTimeout instead of the flat 30s DefaultExecutionTimeout. Without this,
// the watchdog tests would have to wait out a real 30s + abandonGrace before
// the runnerLoop-driven select fires.
const fastExecTimeoutCostEWMAMs = 20.0

// useFastExecTimeout configures the runner's cost-aware scheduling so
// executeJob derives a short execTimeout (~fastExecTimeoutCostEWMAMs ms)
// instead of the flat 30s default, and stamps the same cost onto the given
// job row. This only shrinks execTimeout — the watchdog's grace window is
// separately shrunk via withShortAbandonGrace.
func useFastExecTimeout(runner *CheckWorker, job *models.CheckJob) {
	runner.schedParams.CostTimeoutFactor = 1
	runner.schedParams.CostTimeoutFloor = time.Duration(fastExecTimeoutCostEWMAMs) * time.Millisecond
	job.CostEWMAMs = fastExecTimeoutCostEWMAMs
}

// TestRunnerSurvivesAbandonmentAndProcessesSubsequentJob is the spec's core
// thesis test: "a lost runner is silent, cumulative, and fleet-fatal" — a
// watchdog that only produces a correct result for the hung job but never
// proves the runner goroutine is still alive to pick up more work would miss
// the whole point. This drives one real runnerLoop goroutine, through the
// exact production path (jobsChan -> runnerLoop -> executeJob ->
// runCheckerGuarded), with a hanging job followed immediately by a normal
// job, and asserts the second job completes.
//
//nolint:paralleltest // mutates the package-level abandonGraceOverride
func TestRunnerSurvivesAbandonmentAndProcessesSubsequentJob(t *testing.T) {
	withShortAbandonGrace(t, 50*time.Millisecond)

	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("test-worker", "Test Worker")
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	const hangType = checkerdef.CheckType("test-hanging")
	runner.getChecker, runner.parseConfig = stubResolvers(hangType, newHangingChecker(t))

	startTestRunnerLoop(t, runner, ctx)

	hangCheck := models.NewCheck(org.UID, "watchdog-hang-"+uuid.New().String()[:8], string(hangType))
	require.NoError(t, dbSvc.CreateCheck(ctx, hangCheck))
	hangJob := new(models.CheckJob)
	require.NoError(t, dbSvc.DB().NewSelect().Model(hangJob).Where("check_uid = ?", hangCheck.UID).Scan(ctx))
	claimJobForTest(t, dbSvc, ctx, hangJob, worker.UID)
	useFastExecTimeout(runner, hangJob)

	abandonedBefore := testutil.ToFloat64(prommetrics.CheckRunnerAbandoned.WithLabelValues(string(hangType)))
	gaugeBefore := testutil.ToFloat64(prommetrics.CheckRunnerAbandonedActive)

	select {
	case runner.jobsChan <- hangJob:
	case <-time.After(2 * time.Second):
		t.Fatal("runnerLoop never accepted the hanging job")
	}

	// Wait for exactly one timeout result to land for the hung check —
	// proof the watchdog fired and executeJob's save+release path ran.
	require.Eventually(t, func() bool {
		var n int
		countErr := dbSvc.DB().NewSelect().
			Model((*models.Result)(nil)).
			ColumnExpr("count(*)").
			Where("check_uid = ?", hangCheck.UID).
			Where("status = ?", int(models.ResultStatusTimeout)).
			Scan(ctx, &n)
		require.NoError(t, countErr)

		return n == 1
	}, 3*time.Second, 20*time.Millisecond, "watchdog must abandon the hanging job and save a timeout result")

	abandonedAfter := testutil.ToFloat64(prommetrics.CheckRunnerAbandoned.WithLabelValues(string(hangType)))
	assert.InDelta(t, abandonedBefore+1, abandonedAfter, metricDelta)
	gaugeAfter := testutil.ToFloat64(prommetrics.CheckRunnerAbandonedActive)
	assert.InDelta(t, gaugeBefore+1, gaugeAfter, metricDelta, "the hung goroutine is still leaked at this point")

	// The actual point of the spec: the SAME runnerLoop goroutine (id 0,
	// still looping — no new goroutine was started) must be back at
	// `case job, ok = <-r.jobsChan` ready for more work. Send it a normal
	// heartbeat job and confirm it completes.
	nextJob := newHeartbeatJob(t, dbSvc, ctx, org.UID, worker.UID)
	sendJobAndWaitForResult(t, dbSvc, ctx, runner.jobsChan, nextJob)
}

// TestLateFinishAfterAbandonmentViaRunner drives the D4 discard guarantee
// through the same real jobsChan -> runnerLoop -> executeJob path: the
// abandoned checker eventually unblocks and returns, and that must not
// produce a second result row for the check nor block anything.
//
//nolint:paralleltest // mutates the package-level abandonGraceOverride
func TestLateFinishAfterAbandonmentViaRunner(t *testing.T) {
	withShortAbandonGrace(t, 50*time.Millisecond)

	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("test-worker", "Test Worker")
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	const hangType = checkerdef.CheckType("test-hanging-late")
	unblock := make(chan struct{})
	stub := &hangingChecker{unblock: unblock}
	runner.getChecker, runner.parseConfig = stubResolvers(hangType, stub)

	startTestRunnerLoop(t, runner, ctx)

	hangCheck := models.NewCheck(org.UID, "watchdog-late-"+uuid.New().String()[:8], string(hangType))
	require.NoError(t, dbSvc.CreateCheck(ctx, hangCheck))
	hangJob := new(models.CheckJob)
	require.NoError(t, dbSvc.DB().NewSelect().Model(hangJob).Where("check_uid = ?", hangCheck.UID).Scan(ctx))
	claimJobForTest(t, dbSvc, ctx, hangJob, worker.UID)
	useFastExecTimeout(runner, hangJob)

	gaugeBefore := testutil.ToFloat64(prommetrics.CheckRunnerAbandonedActive)

	select {
	case runner.jobsChan <- hangJob:
	case <-time.After(2 * time.Second):
		t.Fatal("runnerLoop never accepted the hanging job")
	}

	require.Eventually(t, func() bool {
		var n int
		countErr := dbSvc.DB().NewSelect().
			Model((*models.Result)(nil)).
			ColumnExpr("count(*)").
			Where("check_uid = ?", hangCheck.UID).
			Where("status = ?", int(models.ResultStatusTimeout)).
			Scan(ctx, &n)
		require.NoError(t, countErr)

		return n == 1
	}, 3*time.Second, 20*time.Millisecond, "watchdog must abandon and save exactly one timeout result")

	gaugeDuringAbandon := testutil.ToFloat64(prommetrics.CheckRunnerAbandonedActive)
	require.InDelta(t, gaugeBefore+1, gaugeDuringAbandon, metricDelta)

	// Now let the abandoned goroutine finally return. Its send lands in the
	// cap-1 buffer that runCheckerGuarded already stopped reading from (D4).
	close(unblock)

	require.Eventually(t, func() bool {
		return testutil.ToFloat64(prommetrics.CheckRunnerAbandonedActive) == gaugeBefore
	}, 2*time.Second, 10*time.Millisecond, "gauge must decrement once the late child actually returns")

	// No second result must have been written for the check.
	var n int
	require.NoError(t, dbSvc.DB().NewSelect().
		Model((*models.Result)(nil)).
		ColumnExpr("count(*)").
		Where("check_uid = ?", hangCheck.UID).
		Where("status != ?", int(models.ResultStatusCreated)).
		Scan(ctx, &n))
	assert.Equal(t, 1, n, "the late finish must not produce a second result row (D4)")
}

// TestRunnerSurvivesPanicAndProcessesSubsequentJob mirrors the abandonment
// test for D2: a checker panic must not crash the runnerLoop goroutine (or
// the process), and that same goroutine must keep processing jobs.
//
//nolint:paralleltest // mutates the package-level abandonGraceOverride
func TestRunnerSurvivesPanicAndProcessesSubsequentJob(t *testing.T) {
	withShortAbandonGrace(t, 5*time.Second) // grace must not matter: panic returns immediately

	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("test-worker", "Test Worker")
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	const panicType = checkerdef.CheckType("test-panicking")
	runner.getChecker, runner.parseConfig = stubResolvers(panicType, panickingChecker{})

	startTestRunnerLoop(t, runner, ctx)

	panicCheck := models.NewCheck(org.UID, "watchdog-panic-"+uuid.New().String()[:8], string(panicType))
	require.NoError(t, dbSvc.CreateCheck(ctx, panicCheck))
	panicJob := new(models.CheckJob)
	require.NoError(t, dbSvc.DB().NewSelect().Model(panicJob).Where("check_uid = ?", panicCheck.UID).Scan(ctx))
	claimJobForTest(t, dbSvc, ctx, panicJob, worker.UID)

	select {
	case runner.jobsChan <- panicJob:
	case <-time.After(2 * time.Second):
		t.Fatal("runnerLoop never accepted the panicking job")
	}

	// The mere fact this Eventually succeeds (in the same test binary) is
	// itself part of the D2 proof: if recover() had not contained the
	// panic, the whole go test process would have crashed instead of
	// reaching this assertion.
	require.Eventually(t, func() bool {
		var n int
		countErr := dbSvc.DB().NewSelect().
			Model((*models.Result)(nil)).
			ColumnExpr("count(*)").
			Where("check_uid = ?", panicCheck.UID).
			Where("status = ?", int(models.ResultStatusError)).
			Scan(ctx, &n)
		require.NoError(t, countErr)

		return n == 1
	}, 3*time.Second, 20*time.Millisecond, "the panic must produce exactly one error result")

	var errorResults []*models.Result
	require.NoError(t, dbSvc.DB().NewSelect().
		Model(&errorResults).
		Where("check_uid = ?", panicCheck.UID).
		Where("status = ?", int(models.ResultStatusError)).
		Scan(ctx))
	require.Len(t, errorResults, 1)
	assert.Contains(t, errorResults[0].Output[checkerdef.OutputKeyError], "simulated checker panic")

	// The point of the spec: the SAME runnerLoop goroutine survives a
	// checker panic and keeps pulling from jobsChan.
	nextJob := newHeartbeatJob(t, dbSvc, ctx, org.UID, worker.UID)
	sendJobAndWaitForResult(t, dbSvc, ctx, runner.jobsChan, nextJob)
}

// instantConfig/instantChecker are a trivial non-passive checker that
// returns immediately — used to drive executeJob's real sleep-accounting
// path (D5) without a hanging/panicking stub or a real network check.
type instantConfig struct{}

func (instantConfig) FromMap(map[string]any) error { return nil }
func (instantConfig) GetConfig() map[string]any    { return map[string]any{} }

type instantChecker struct{}

func (instantChecker) Type() checkerdef.CheckType           { return checkerdef.CheckType("test-instant") }
func (instantChecker) Validate(*checkerdef.CheckSpec) error { return nil }

func (instantChecker) Execute(context.Context, checkerdef.Config) (*checkerdef.Result, error) {
	return &checkerdef.Result{Status: checkerdef.StatusUp}, nil
}

func instantResolvers() (
	func(checkerdef.CheckType) (checkerdef.Checker, bool),
	func(checkerdef.CheckType) (checkerdef.Config, bool),
) {
	const instantType = checkerdef.CheckType("test-instant")

	getChecker := func(ct checkerdef.CheckType) (checkerdef.Checker, bool) {
		if ct == instantType {
			return instantChecker{}, true
		}

		return registry.GetChecker(ct)
	}
	parseConfig := func(ct checkerdef.CheckType) (checkerdef.Config, bool) {
		if ct == instantType {
			return instantConfig{}, true
		}

		return registry.ParseConfig(ct)
	}

	return getChecker, parseConfig
}

// TestExecuteJobParkedCountTracksPreScheduleSleep covers D5 (spec
// 2026-07-05-08): a job claimed ahead of its scheduled_at bumps the parked
// counter for the duration of the pre-schedule sleep and releases it back to
// 0 once the sleep resolves — visibility into how much of the pool the
// bounded claim-ahead window (D3) is occupying at any instant.
//
//nolint:paralleltest // shares runner/DB state via startTestRunnerLoop
func TestExecuteJobParkedCountTracksPreScheduleSleep(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("test-worker", "Test Worker")
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	runner.getChecker, runner.parseConfig = instantResolvers()

	startTestRunnerLoop(t, runner, ctx)

	require.Zero(t, runner.parked.Load(), "parked must start at 0")

	check := models.NewCheck(org.UID, "parked-"+uuid.New().String()[:8], "test-instant")
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	job := new(models.CheckJob)
	require.NoError(t, dbSvc.DB().NewSelect().Model(job).Where("check_uid = ?", check.UID).Scan(ctx))

	// Claimed well ahead of its own scheduled_at (as D3's bounded window
	// still allows for a short-period job) — executeJob must sleep before
	// running the checker.
	scheduledAt := time.Now().Add(300 * time.Millisecond)
	_, err = dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", scheduledAt).
		Where("uid = ?", job.UID).
		Exec(ctx)
	require.NoError(t, err)
	job.ScheduledAt = &scheduledAt

	claimJobForTest(t, dbSvc, ctx, job, worker.UID)

	select {
	case runner.jobsChan <- job:
	case <-time.After(2 * time.Second):
		t.Fatal("runnerLoop never accepted the job")
	}

	// While the job is sleeping toward its scheduled_at, parked must read 1.
	require.Eventually(t, func() bool {
		return runner.parked.Load() == 1
	}, 250*time.Millisecond, 5*time.Millisecond, "parked must be 1 while executeJob sleeps toward scheduled_at")

	// Once the checker actually runs and the job completes, parked must
	// release back to 0 — proof the counter reflects "sleeping", not "holds
	// a lease" (the lease is held for the whole executeJob call, but parked
	// only for the sleep portion of it).
	require.Eventually(t, func() bool {
		return runner.parked.Load() == 0
	}, 2*time.Second, 10*time.Millisecond, "parked must release back to 0 once the sleep resolves")

	// Confirm the job actually ran to completion (result saved, lease
	// released) rather than parked staying 0 because the job was dropped.
	require.Eventually(t, func() bool {
		var n int
		countErr := dbSvc.DB().NewSelect().
			Model((*models.Result)(nil)).
			ColumnExpr("count(*)").
			Where("check_uid = ?", check.UID).
			Where("status = ?", int(models.ResultStatusUp)).
			Scan(ctx, &n)
		require.NoError(t, countErr)

		return n == 1
	}, 2*time.Second, 10*time.Millisecond, "the instant checker must have produced exactly one UP result")
}

// TestExecuteJobPassiveJobHonorsScheduledAtSleep is a regression test: passive
// (heartbeat/email) jobs used to be dispatched to executePassiveJob before
// the pre-schedule sleep that active checks already honored, so a job
// re-claimed within D3's bounded claim-ahead window fired immediately on
// every release instead of waiting out the window — thundering-herding the
// pool with repeated executions (and repeated live-update hints) until the
// phase-locked tick actually arrived. executeJob must now sleep toward
// scheduled_at for passive jobs exactly like it does for active ones.
//
//nolint:paralleltest // Test uses shared database state
func TestExecuteJobPassiveJobHonorsScheduledAtSleep(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("test-worker", "Test Worker")
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	startTestRunnerLoop(t, runner, ctx)

	require.Zero(t, runner.parked.Load(), "parked must start at 0")

	check := models.NewCheck(org.UID, "passive-parked-"+uuid.New().String()[:8], string(checkerdef.CheckTypeHeartbeat))
	check.Config = models.JSONMap{"token": "passive-parked-token"}
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	job := new(models.CheckJob)
	require.NoError(t, dbSvc.DB().NewSelect().Model(job).Where("check_uid = ?", check.UID).Scan(ctx))

	// Claimed well ahead of its own scheduled_at, mirroring D3's bounded
	// claim-ahead window on a real pool claim.
	scheduledAt := time.Now().Add(300 * time.Millisecond)
	_, err = dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", scheduledAt).
		Where("uid = ?", job.UID).
		Exec(ctx)
	require.NoError(t, err)
	job.ScheduledAt = &scheduledAt

	claimJobForTest(t, dbSvc, ctx, job, worker.UID)

	select {
	case runner.jobsChan <- job:
	case <-time.After(2 * time.Second):
		t.Fatal("runnerLoop never accepted the job")
	}

	// While the job is sleeping toward its scheduled_at, parked must read 1
	// and no result must exist yet — proof the passive job didn't fire on claim.
	require.Eventually(t, func() bool {
		return runner.parked.Load() == 1
	}, 250*time.Millisecond, 5*time.Millisecond, "parked must be 1 while executeJob sleeps toward scheduled_at")

	// The check's seeded placeholder ("created") result row exists from the
	// moment it's created — only a real (non-"created") result proves the
	// passive job actually ran.
	var early int
	require.NoError(t, dbSvc.DB().NewSelect().
		Model((*models.Result)(nil)).
		ColumnExpr("count(*)").
		Where("check_uid = ?", check.UID).
		Where("status != ?", int(models.ResultStatusCreated)).
		Scan(ctx, &early))
	require.Zero(t, early, "the passive job must not produce a result before its scheduled_at arrives")

	require.Eventually(t, func() bool {
		return runner.parked.Load() == 0
	}, 2*time.Second, 10*time.Millisecond, "parked must release back to 0 once the sleep resolves")

	require.Eventually(t, func() bool {
		var n int
		countErr := dbSvc.DB().NewSelect().
			Model((*models.Result)(nil)).
			ColumnExpr("count(*)").
			Where("check_uid = ?", check.UID).
			Where("status != ?", int(models.ResultStatusCreated)).
			Scan(ctx, &n)
		require.NoError(t, countErr)

		return n == 1
	}, 2*time.Second, 10*time.Millisecond, "the passive job must produce exactly one result once it actually runs")
}

// TestNextPollDelay pins the fetcher's hint-driven wake (spec 2026-07-20-03):
// before the hint existed, the only periodic wake was the flat fallback poll,
// so on an idle worker a 10s-period job (claimable only 5s before due) ran
// once per minute.
func TestNextPollDelay(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(fallbackPollInterval, nextPollDelay(0), "no hint falls back to the flat poll")
	r.Equal(fallbackPollInterval, nextPollDelay(-time.Second), "a negative hint falls back")
	r.Equal(fallbackPollInterval, nextPollDelay(2*time.Minute), "a hint past the fallback is capped by it")
	r.Equal(fallbackPollInterval, nextPollDelay(fallbackPollInterval), "an exactly-fallback hint stays the fallback")
	r.Equal(5*time.Second, nextPollDelay(5*time.Second), "a sub-minute hint is honored as-is")
	r.Equal(minPollDelay, nextPollDelay(time.Millisecond), "a tiny hint is floored so the fetcher cannot spin")
}
