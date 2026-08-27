package checkjobsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// Fairness simulation parameters (spec 2026-08-26-02). They reproduce the shape
// of the production incident: an org scheduling twice what its per-minute cap
// allows, with deterministic per-check phases clustered into one burst.
const (
	// fairnessChecks is how many 1-minute checks the org schedules.
	fairnessChecks = 8
	// fairnessCap is how many of them the per-org token bucket lets through in
	// one window — half of demand, i.e. the org runs at 2x its
	// MaxChecksPerMinute, exactly like the org in the incident.
	fairnessCap = 4
	// fairnessWindows is how many periods the simulation runs.
	fairnessWindows = 12
	// fairnessPeriod is the checks' period (models.NewCheck's default).
	fairnessPeriod = time.Minute
	// fairnessPhaseStep clusters every check's phase into the first seconds of
	// the period, one second apart. This is the adversarial case: in production
	// the phase is a stable hash of the check UID, so the arrival order never
	// changes on its own — whoever sorts last loses every window forever unless
	// something rotates the deficit.
	fairnessPhaseStep = time.Second
)

// deferralFunc is the lease-release rung under test: the fixed
// DeferLeaseRateLimited, or the legacy ReleaseLease whose re-anchoring of
// effective_scheduled_at caused the starvation.
type deferralFunc func(ctx context.Context, jobUID, workerUID string, nextScheduledAt time.Time) error

// fairnessFixture is one simulated organization: fairnessChecks jobs sharing a
// region, plus the virtual timeline their ticks live on.
type fairnessFixture struct {
	ctx     context.Context //nolint:containedctx // test fixture, mirrors setupTestDB's returned ctx
	dbSvc   *sqlite.Service
	svc     checkjobsvc.Service
	org     *models.Organization
	worker  *models.Worker
	region  string
	jobs    []*models.CheckJob
	indexOf map[string]int
	base    time.Time
}

// newFairnessFixture builds a dedicated in-memory database so each simulation
// claims only its own jobs (the cloud claim lane also matches NULL-region rows,
// so sharing a DB across sub-cases would leak jobs between them).
func newFairnessFixture(t *testing.T) *fairnessFixture {
	t.Helper()

	dbSvc, ctx := setupTestDB(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	fixture := &fairnessFixture{
		ctx:     ctx,
		dbSvc:   dbSvc,
		svc:     checkjobsvc.NewService(dbSvc.DB()),
		org:     createTestOrg(t, ctx, dbSvc),
		region:  "fairness-region",
		indexOf: make(map[string]int, fairnessChecks),
		// The whole simulation sits in the past so every window's ticks are
		// already due. What this test exercises is the ORDER of a saturated
		// due-batch, not the wall-clock claim gate (covered by
		// TestClaimJobsBoundedClaimAheadWindow). Whole seconds only: the SQLite
		// dialect stores timestamps as text, and mixing sub-second precision
		// into the ORDER BY key would make the comparison lexical, not temporal.
		base: time.Now().Add(-2 * time.Hour).Truncate(time.Second),
	}

	fixture.worker = createTestWorker(t, ctx, dbSvc, &fixture.region)

	for i := range fairnessChecks {
		job := createTestCheckJob(t, ctx, dbSvc, fixture.org.UID, fixture.tick(i, 0), &fixture.region)
		fixture.jobs = append(fixture.jobs, job)
		fixture.indexOf[job.UID] = i
	}

	return fixture
}

// tick is the virtual scheduled_at of check `index` in `window`.
func (f *fairnessFixture) tick(index, window int) time.Time {
	return f.base.
		Add(time.Duration(window) * fairnessPeriod).
		Add(time.Duration(index) * fairnessPhaseStep)
}

// nextTick is the tick a job released during `window` is rescheduled to.
func (f *fairnessFixture) nextTick(jobUID string, window int) time.Time {
	return f.tick(f.indexOf[jobUID], window+1)
}

// claimInProcess is the cloud worker claim path (CheckWorker's fetcher).
func (f *fairnessFixture) claimInProcess(t *testing.T) []*models.CheckJob {
	t.Helper()

	jobs, _, err := f.svc.ClaimJobs(
		f.ctx, f.worker.UID, &f.region, fairnessChecks, fairnessChecks, 5*time.Minute,
	)
	require.NoError(t, err)

	return jobs
}

// claimAsAgent is the deported-agent claim path (agentws.handleClaim), which
// enforces the same per-org cap at dispatch and must rotate identically.
func (f *fairnessFixture) claimAsAgent(t *testing.T) []*models.CheckJob {
	t.Helper()

	jobs, _, err := f.svc.ClaimJobsForAgent(
		f.ctx, f.worker.UID,
		checkjobsvc.AgentScope{OrgUID: f.org.UID, Region: f.region},
		"", fairnessChecks, 5*time.Minute,
	)
	require.NoError(t, err)

	return jobs
}

// simulate runs fairnessWindows periods. Each window claims the whole due batch
// (as the fetcher does), then walks it in claim order spending a budget of
// fairnessCap executions — the token bucket's per-window allowance. Winners go
// through the real post-exec release; everyone after the budget runs out goes
// through the deferral under test. Returns executions per job UID.
func (f *fairnessFixture) simulate(
	t *testing.T,
	claim func(*testing.T) []*models.CheckJob,
	deferJob deferralFunc,
) map[string]int {
	t.Helper()

	executions := make(map[string]int, len(f.jobs))
	for _, job := range f.jobs {
		executions[job.UID] = 0
	}

	for window := range fairnessWindows {
		claimed := claim(t)
		require.Len(t, claimed, fairnessChecks,
			"window %d: every job is past due, so the whole batch must be claimed", window)

		budget := fairnessCap

		for _, job := range claimed {
			next := f.nextTick(job.UID, window)

			if budget == 0 {
				// Bucket drained — this is the write path under test.
				require.NoError(t, deferJob(f.ctx, job.UID, f.worker.UID, next))

				continue
			}

			budget--
			executions[job.UID]++

			// A real execution always releases through the post-exec write,
			// which re-anchors the ordering key to the new schedule. That is
			// precisely the behavior a deferral must NOT copy.
			require.NoError(t, f.svc.ReleaseLeaseWithSchedulingState(
				f.ctx, job.UID, f.worker.UID, next, 0, 0, next, scheduling.LaneFast,
			))
		}
	}

	return executions
}

// TestRateLimitedOrgRotatesTheDeficit is the fairness regression test of spec
// 2026-08-26-02, run over both claim paths.
//
// The two "LegacyReAnchor" cases are POSITIVE CONTROLS: they drive the exact
// same harness through the old re-anchoring release and assert that it starves
// half the org outright — one check at 0 executions for the whole simulation,
// which is the 7.5-hours-of-nothing signature from production. Without them, a
// harness that simply never saturated the bucket would "prove" fairness by
// accident. Point the fixed deferral at those expectations and they fail, and
// point the legacy one at the fairness expectations and those fail: the two
// halves are mutually falsifying.
//
//nolint:paralleltest // each case builds its own in-memory DB, like the rest of this package
func TestRateLimitedOrgRotatesTheDeficit(t *testing.T) {
	cases := []struct {
		name        string
		agentClaim  bool
		legacyDefer bool
		wantStarved int
	}{
		{name: "InProcessClaim_FixedDeferralRotates", wantStarved: 0},
		{
			name: "InProcessClaim_LegacyReAnchorStarves", legacyDefer: true,
			wantStarved: fairnessChecks - fairnessCap,
		},
		{name: "AgentClaim_FixedDeferralRotates", agentClaim: true, wantStarved: 0},
		{
			name: "AgentClaim_LegacyReAnchorStarves", agentClaim: true, legacyDefer: true,
			wantStarved: fairnessChecks - fairnessCap,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) { //nolint:paralleltest // own in-memory DB per case
			fixture := newFairnessFixture(t)

			claim := fixture.claimInProcess
			if testCase.agentClaim {
				claim = fixture.claimAsAgent
			}

			deferJob := fixture.svc.DeferLeaseRateLimited
			if testCase.legacyDefer {
				deferJob = fixture.svc.ReleaseLease
			}

			executions := fixture.simulate(t, claim, deferJob)

			starved, total, best := 0, 0, 0

			for _, job := range fixture.jobs {
				count := executions[job.UID]
				total += count

				if count == 0 {
					starved++
				}

				if count > best {
					best = count
				}
			}

			// Both variants burn the identical amount of capacity — the fix
			// redistributes executions, it does not create or destroy any.
			require.Equal(t, fairnessWindows*fairnessCap, total,
				"the per-window budget must be fully spent in every window")

			require.Equal(t, testCase.wantStarved, starved,
				"expected %d check(s) with zero executions over %d windows, got %d (per-check: %v)",
				testCase.wantStarved, fairnessWindows, starved, executions)

			if testCase.wantStarved > 0 {
				// Control: the incident's signature is not "everyone degrades"
				// but "half run perfectly, half never run at all".
				require.Equal(t, fairnessWindows, best,
					"re-anchoring lets the early-phase checks run every single window")

				return
			}

			// Fairness: every check lands within one execution of its share of
			// the org's capacity — cap/demand of its configured rate.
			wantEach := fairnessWindows * fairnessCap / fairnessChecks

			for _, job := range fixture.jobs {
				require.InDelta(t, wantEach, executions[job.UID], 1,
					"check at phase +%s must get its fair share of the org's capacity",
					time.Duration(fixture.indexOf[job.UID])*fairnessPhaseStep)
			}
		})
	}
}

// TestDeferLeaseRateLimitedPreservesOrderingKey pins the write-level contract of
// the deferral, and the no-regression half: a SUCCESSFUL execution still
// re-anchors effective_scheduled_at to the new schedule.
//
//nolint:paralleltest // Test shares database state
func TestDeferLeaseRateLimitedPreservesOrderingKey(t *testing.T) {
	dbSvc, ctx := setupTestDB(t)
	defer func() { _ = dbSvc.Close() }()

	svc := checkjobsvc.NewService(dbSvc.DB())
	org := createTestOrg(t, ctx, dbSvc)

	// lease claims a job for worker so the ownership guard is satisfied.
	lease := func(t *testing.T, jobUID, workerUID string, expiry time.Time) {
		t.Helper()

		_, err := dbSvc.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("lease_worker_uid = ?", workerUID).
			Set("lease_expires_at = ?", expiry).
			Set("lease_starts = ?", 1).
			Where("uid = ?", jobUID).
			Exec(ctx)
		require.NoError(t, err)
	}

	reload := func(t *testing.T, jobUID string) models.CheckJob {
		t.Helper()

		var dbJob models.CheckJob

		require.NoError(t, dbSvc.DB().NewSelect().Model(&dbJob).Where("uid = ?", jobUID).Scan(ctx))

		return dbJob
	}

	t.Run("AdvancesScheduleButNotTheOrderingKey", func(t *testing.T) { //nolint:paralleltest // shares DB
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()
		missedTick := now.Add(-10 * time.Second)
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, missedTick, nil)

		// Give the job scheduling telemetry a deferral must not clobber.
		_, err := dbSvc.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("cost_ewma_ms = ?", 4321.0).
			Set("delay_ewma_ms = ?", 8765.0).
			Set("lane = ?", scheduling.LaneSlow).
			Where("uid = ?", job.UID).
			Exec(ctx)
		require.NoError(t, err)

		lease(t, job.UID, worker.UID, now.Add(time.Minute))

		nextTick := missedTick.Add(time.Minute)
		require.NoError(t, svc.DeferLeaseRateLimited(ctx, job.UID, worker.UID, nextTick))

		dbJob := reload(t, job.UID)

		require.NotNil(t, dbJob.ScheduledAt)
		require.WithinDuration(t, nextTick, *dbJob.ScheduledAt, time.Second,
			"scheduled_at must advance so the job cannot be re-claimed in the same window")

		require.NotNil(t, dbJob.EffectiveScheduledAt)
		require.WithinDuration(t, missedTick, *dbJob.EffectiveScheduledAt, time.Second,
			"effective_scheduled_at must stay at the missed tick — that is the whole fix")

		require.Nil(t, dbJob.LeaseWorkerUID, "the lease must be released")
		require.Nil(t, dbJob.LeaseExpiresAt, "the lease expiry must be cleared")
		require.Equal(t, 0, dbJob.LeaseStarts, "no probe started, so this is not a crash attempt")

		require.InDelta(t, 4321.0, dbJob.CostEWMAMs, 0.001, "no probe ran: no cost sample to fold in")
		require.InDelta(t, 8765.0, dbJob.DelayEWMAMs, 0.001,
			"the delay EWMA is the diagnostic that exposed this incident; a deferral must not reset it")
		require.Equal(t, scheduling.LaneSlow, dbJob.Lane, "no cost sample means no reclassification")
	})

	t.Run("RepeatedDeferralsKeepReceding", func(t *testing.T) { //nolint:paralleltest // shares DB
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()
		missedTick := now.Add(-10 * time.Second)
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, missedTick, nil)

		for i := 1; i <= 3; i++ {
			lease(t, job.UID, worker.UID, now.Add(time.Minute))
			require.NoError(t, svc.DeferLeaseRateLimited(
				ctx, job.UID, worker.UID, missedTick.Add(time.Duration(i)*time.Minute),
			))
		}

		dbJob := reload(t, job.UID)
		require.NotNil(t, dbJob.EffectiveScheduledAt)
		require.WithinDuration(t, missedTick, *dbJob.EffectiveScheduledAt, time.Second,
			"repeated deferrals must leave the anchor untouched so overdue-ness keeps growing")
	})

	t.Run("RejectsAnotherWorkersLease", func(t *testing.T) { //nolint:paralleltest // shares DB
		worker := createTestWorker(t, ctx, dbSvc, nil)
		other := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)

		lease(t, job.UID, other.UID, now.Add(time.Minute))

		require.Error(t, svc.DeferLeaseRateLimited(ctx, job.UID, worker.UID, now.Add(time.Minute)),
			"the deferral keeps the same ownership guard as every other release")
	})

	t.Run("SuccessfulExecutionStillReAnchors", func(t *testing.T) { //nolint:paralleltest // shares DB
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)

		lease(t, job.UID, worker.UID, now.Add(time.Minute))

		nextTick := now.Add(time.Minute)
		require.NoError(t, svc.ReleaseLeaseWithSchedulingState(
			ctx, job.UID, worker.UID, nextTick, 12.0, 34.0, nextTick, scheduling.LaneFast,
		))

		dbJob := reload(t, job.UID)
		require.NotNil(t, dbJob.EffectiveScheduledAt)
		require.WithinDuration(t, nextTick, *dbJob.EffectiveScheduledAt, time.Second,
			"a check that actually ran starts its next period with no accumulated overdue-ness")
	})
}
