package checkjobsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

func setupTestDB(t *testing.T) (*sqlite.Service, context.Context) {
	t.Helper()

	ctx := context.Background()

	svc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err, "failed to create in-memory database")

	err = svc.Initialize(ctx)
	require.NoError(t, err, "failed to initialize database")

	return svc, ctx
}

//nolint:revive // Test helper function, context parameter order is acceptable
func createTestOrg(t *testing.T, ctx context.Context, svc *sqlite.Service) *models.Organization {
	t.Helper()

	org := models.NewOrganization("test-org", "")
	err := svc.CreateOrganization(ctx, org)
	require.NoError(t, err, "failed to create organization")

	return org
}

//nolint:revive // Test helper function, context parameter order is acceptable
func createTestWorker(t *testing.T, ctx context.Context, svc *sqlite.Service, region *string) *models.Worker {
	t.Helper()

	slug := "test-worker-" + uuid.New().String()[:8]
	worker := models.NewWorker(slug, "Test Worker")
	worker.Region = region
	_, err := svc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err, "failed to create worker")

	return worker
}

//nolint:revive // Test helper function, context parameter order is acceptable
func createTestCheckJob(
	t *testing.T,
	ctx context.Context,
	svc *sqlite.Service,
	orgUID string,
	scheduledAt time.Time,
	region *string,
) *models.CheckJob {
	t.Helper()

	// First create a check (this automatically creates a check_job)
	check := models.NewCheck(orgUID, "test-check-"+uuid.New().String()[:8], "http")
	err := svc.CreateCheck(ctx, check)
	require.NoError(t, err, "failed to create check")

	// Retrieve the automatically created check_job
	job := new(models.CheckJob)
	err = svc.DB().NewSelect().
		Model(job).
		Where("check_uid = ?", check.UID).
		Scan(ctx)
	require.NoError(t, err, "failed to retrieve check job")

	// Update the job with test-specific values. Keep effective_scheduled_at in
	// step with scheduled_at (a fresh job carries no cost/delay offset), mirroring
	// production where every release writes both — the claim gate/order key is
	// effective_scheduled_at, not scheduled_at.
	_, err = svc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", scheduledAt).
		Set("effective_scheduled_at = ?", scheduledAt).
		Set("region = ?", region).
		Where("uid = ?", job.UID).
		Exec(ctx)
	require.NoError(t, err, "failed to update check job")

	// Refresh the job to get updated values
	job.ScheduledAt = &scheduledAt
	job.EffectiveScheduledAt = &scheduledAt
	job.Region = region

	return job
}

//nolint:paralleltest // Test shares database state
func TestClaimJobs(t *testing.T) {
	dbSvc, ctx := setupTestDB(t)
	defer func() { _ = dbSvc.Close() }()

	svc := checkjobsvc.NewService(dbSvc.DB())
	org := createTestOrg(t, ctx, dbSvc)

	t.Run("ClaimAvailableJobs", func(t *testing.T) { //nolint:paralleltest // Test shares database state
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// Create jobs that should be claimed
		job1 := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)
		_ = createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-5*time.Second), nil)

		// Claim jobs
		jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
		require.NoError(t, err)
		require.Len(t, jobs, 2, "should claim 2 jobs")

		// Verify jobs are claimed
		assert.NotNil(t, jobs[0].LeaseWorkerUID)
		assert.NotNil(t, jobs[0].LeaseExpiresAt)
		assert.Equal(t, worker.UID, *jobs[0].LeaseWorkerUID)
		assert.Equal(t, 1, jobs[0].LeaseStarts)

		// Verify ordering (earliest scheduled_at first)
		assert.True(t, jobs[0].ScheduledAt.Before(*jobs[1].ScheduledAt))

		// Verify lease expiry is calculated correctly
		// When job is behind schedule (scheduled in the past), lease is based on now, not scheduled_at
		// This prevents the lease from expiring too soon
		latest := *jobs[0].ScheduledAt
		if now.After(latest) {
			latest = now
		}
		expectedExpiry := latest.Add(1*time.Minute + 30*time.Second)
		assert.WithinDuration(t, expectedExpiry, *jobs[0].LeaseExpiresAt, 2*time.Second)

		// Verify jobs are updated in database
		var dbJob models.CheckJob
		err = dbSvc.DB().NewSelect().Model(&dbJob).Where("uid = ?", job1.UID).Scan(ctx)
		require.NoError(t, err)
		assert.Equal(t, worker.UID, *dbJob.LeaseWorkerUID)
		assert.Equal(t, 1, dbJob.LeaseStarts)
	})

	t.Run("RespectLimit", func(t *testing.T) { //nolint:paralleltest // Test shares database state
		worker := createTestWorker(t, ctx, dbSvc, nil)
		// Create 5 jobs
		now := time.Now()
		for i := 0; i < 5; i++ {
			createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-time.Duration(i)*time.Second), nil)
		}

		// Claim with limit of 3
		jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 3, 3, 5*time.Minute)
		require.NoError(t, err)
		assert.Len(t, jobs, 3, "should respect limit")
	})

	t.Run("SkipJobsWithActiveLease", func(t *testing.T) { //nolint:paralleltest // Test shares database state
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// First, claim and clear any existing unclaimed jobs to avoid interference
		existingJobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 100, 100, 5*time.Minute)
		require.NoError(t, err)
		// Mark them all as released with far future schedule so they don't interfere
		for _, existingJob := range existingJobs {
			_ = svc.ReleaseLease(ctx, existingJob.UID, worker.UID, now.Add(1*time.Hour))
		}

		// Create job with active lease
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)
		otherWorker := createTestWorker(t, ctx, dbSvc, nil)

		leaseExpiry := now.Add(30 * time.Second)
		_, err = dbSvc.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("lease_worker_uid = ?", otherWorker.UID).
			Set("lease_expires_at = ?", leaseExpiry).
			Where("uid = ?", job.UID).
			Exec(ctx)
		require.NoError(t, err)

		// Try to claim jobs
		jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
		require.NoError(t, err)
		assert.Empty(t, jobs, "should not claim jobs with active lease")
	})

	t.Run("ClaimJobsWithExpiredLease", func(t *testing.T) {
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// Create job with expired lease
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)
		otherWorker := createTestWorker(t, ctx, dbSvc, nil)

		expiredTime := now.Add(-10 * time.Second)
		_, err := dbSvc.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("lease_worker_uid = ?", otherWorker.UID).
			Set("lease_expires_at = ?", expiredTime).
			Set("lease_starts = ?", 1).
			Where("uid = ?", job.UID).
			Exec(ctx)
		require.NoError(t, err)

		// Try to claim jobs
		jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
		require.NoError(t, err)
		require.Len(t, jobs, 1, "should claim job with expired lease")

		// Verify lease_starts was incremented
		assert.Equal(t, 2, jobs[0].LeaseStarts, "lease_starts should be incremented")
	})

	t.Run("RegionMatching", func(t *testing.T) {
		now := time.Now()
		euRegion := "eu-west-1"
		usRegion := "us-east-1"

		// Create jobs with different regions
		globalJob := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)
		euJob := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-9*time.Second), &euRegion)
		usJob := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-8*time.Second), &usRegion)

		// Worker with EU region should claim global and EU jobs
		euWorker := createTestWorker(t, ctx, dbSvc, &euRegion)
		jobs, _, err := svc.ClaimJobs(ctx, euWorker.UID, &euRegion, 10, 10, 5*time.Minute)
		require.NoError(t, err)
		assert.Len(t, jobs, 2, "EU worker should claim global and EU jobs")

		// Verify it claimed the right jobs
		claimedUIDs := []string{jobs[0].UID, jobs[1].UID}
		assert.Contains(t, claimedUIDs, globalJob.UID)
		assert.Contains(t, claimedUIDs, euJob.UID)
		assert.NotContains(t, claimedUIDs, usJob.UID)
	})

	t.Run("MaxAheadWindow", func(t *testing.T) {
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// First, claim and clear any existing jobs to avoid interference
		existingJobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 100, 100, 10*time.Minute)
		require.NoError(t, err)
		// Mark them all as released with far future schedule so they don't interfere
		for _, existingJob := range existingJobs {
			_ = svc.ReleaseLease(ctx, existingJob.UID, worker.UID, now.Add(2*time.Hour))
		}

		// Create job far in the future (beyond max_ahead window)
		_ = createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(10*time.Minute), nil)

		// Try to claim with 5 minute max_ahead - should not claim the far future job
		jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
		require.NoError(t, err)
		assert.Empty(t, jobs, "should not claim jobs beyond max_ahead")

		// Create job that is due now (within window)
		nearJob := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)

		// Should claim the near job
		jobs, _, err = svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
		require.NoError(t, err)
		require.Len(t, jobs, 1, "should claim job that is due")
		assert.Equal(t, nearJob.UID, jobs[0].UID)
	})

	t.Run("NoJobsAvailable", func(t *testing.T) {
		// Claim when no jobs exist
		newWorker := createTestWorker(t, ctx, dbSvc, nil)
		jobs, _, err := svc.ClaimJobs(ctx, newWorker.UID, nil, 10, 10, 5*time.Minute)
		require.NoError(t, err)
		assert.Empty(t, jobs, "should return empty slice when no jobs available")
	})

	t.Run("AttachesCheckAtClaimTime", func(t *testing.T) { //nolint:paralleltest // Test shares database state
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)
		createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-9*time.Second), nil)

		jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(jobs), 2)

		for _, j := range jobs {
			require.NotNil(t, j.Check, "every claimed job must have its check attached")
			assert.Equal(t, j.CheckUID, j.Check.UID, "attached check must match the job's check_uid")
			assert.Equal(t, j.OrganizationUID, j.Check.OrganizationUID)
		}
	})
}

// setEffective stamps the cost-aware ordering fields on a job so the claim
// SELECT's ORDER BY effective_scheduled_at can be exercised at the DB level.
//
//nolint:revive // Test helper function, context parameter order is acceptable
func setEffective(t *testing.T, ctx context.Context, svc *sqlite.Service, jobUID string, effective time.Time) {
	t.Helper()

	_, err := svc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("effective_scheduled_at = ?", effective).
		Where("uid = ?", jobUID).
		Exec(ctx)
	require.NoError(t, err, "failed to set effective_scheduled_at")
}

// TestClaimOrdersByEffectiveScheduledAt verifies the WFQ ordering (spec
// 2026-06-30-09, D2/Option A): all jobs are due (gate is on scheduled_at) but
// the claim picks them in effective_scheduled_at order, so a fast/paid job whose
// effective deadline is earlier is claimed ahead of a slow/free job that became
// due at the same wall-clock time.
//
//nolint:paralleltest // Test shares database state
func TestClaimOrdersByEffectiveScheduledAt(t *testing.T) {
	dbSvc, ctx := setupTestDB(t)
	defer func() { _ = dbSvc.Close() }()

	svc := checkjobsvc.NewService(dbSvc.DB())
	org := createTestOrg(t, ctx, dbSvc)
	worker := createTestWorker(t, ctx, dbSvc, nil)

	now := time.Now()
	// Both jobs are due now (same scheduled_at), so the gate admits both.
	slowFree := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-1*time.Second), nil)
	fastPaid := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-1*time.Second), nil)

	// The slow/free job carries a later effective deadline (penalized); the
	// fast/paid job an earlier one (credited).
	setEffective(t, ctx, dbSvc, slowFree.UID, now.Add(20*time.Second))
	setEffective(t, ctx, dbSvc, fastPaid.UID, now.Add(-10*time.Second))

	// Claim a single slot under contention: the earlier effective deadline wins.
	jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 1, 1, 5*time.Minute)
	require.NoError(t, err)
	require.Len(t, jobs, 1, "should claim exactly one job under the limit")
	assert.Equal(t, fastPaid.UID, jobs[0].UID,
		"the job with the earlier effective_scheduled_at must be claimed first")
}

// TestClaimAdmitsDueJobRegardlessOfEffective verifies the restored D2/Option A
// gate (spec 2026-07-01-02 D3): eligibility is decided by the real
// scheduled_at alone, so a due job is claimable IMMEDIATELY no matter what its
// stored effective_scheduled_at says — including a polluted pre-migration row
// carrying a delay-era offset far beyond the claim window (~95 min was
// observed live). effective_scheduled_at only orders the due set; it can never
// strand a job.
//
//nolint:paralleltest // Test shares database state
func TestClaimAdmitsDueJobRegardlessOfEffective(t *testing.T) {
	dbSvc, ctx := setupTestDB(t)
	defer func() { _ = dbSvc.Close() }()

	svc := checkjobsvc.NewService(dbSvc.DB())
	org := createTestOrg(t, ctx, dbSvc)
	worker := createTestWorker(t, ctx, dbSvc, nil)

	now := time.Now()
	maxAhead := 5 * time.Minute

	// The job is due now, but a pre-migration delay-era offset left its
	// effective deadline ~95 minutes in the future — 19× the claim window.
	job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-1*time.Second), nil)
	setEffective(t, ctx, dbSvc, job.UID, now.Add(95*time.Minute))

	jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, maxAhead)
	require.NoError(t, err)
	require.Len(t, jobs, 1, "a due job must be claimable immediately regardless of any stored effective value")
	assert.Equal(t, job.UID, jobs[0].UID)
}

// markSlow stamps a job into the slow lane so the per-lane claim SELECTs can
// be exercised at the DB level (production classifies lanes in the post-exec
// release; new jobs are lane-fast by default).
//
//nolint:revive // Test helper function, context parameter order is acceptable
func markSlow(t *testing.T, ctx context.Context, svc *sqlite.Service, jobUID string) {
	t.Helper()

	_, err := svc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("lane = ?", scheduling.LaneSlow).
		Where("uid = ?", jobUID).
		Exec(ctx)
	require.NoError(t, err, "failed to set lane")
}

// TestClaimJobsLaneReservation exercises the per-lane reservation claim (spec
// 2026-07-01-03 D3/D6): the slow lane claims its budgeted share first (capped
// by slowLimit and the free capacity), fast fills every remaining slot, and an
// idle fast stream lets slow borrow the full budget. Slow-first is the
// anti-starvation property — see SaturatedFastLaneCannotStarveSlow.
//
//nolint:paralleltest // Test shares database state
func TestClaimJobsLaneReservation(t *testing.T) {
	dbSvc, ctx := setupTestDB(t)
	defer func() { _ = dbSvc.Close() }()

	svc := checkjobsvc.NewService(dbSvc.DB())
	org := createTestOrg(t, ctx, dbSvc)

	// countByLane returns (fast, slow) claim counts.
	countByLane := func(jobs []*models.CheckJob) (int, int) {
		fast, slow := 0, 0
		for _, j := range jobs {
			if j.Lane == scheduling.LaneSlow {
				slow++
			} else {
				fast++
			}
		}

		return fast, slow
	}

	t.Run("SlowCappedByBudgetFastFillsRest", func(t *testing.T) { //nolint:paralleltest // shares DB
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// 4 due slow jobs, 3 due fast jobs; capacity 4, slow budget 1.
		for i := 0; i < 4; i++ {
			j := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)
			markSlow(t, ctx, dbSvc, j.UID)
		}
		for i := 0; i < 3; i++ {
			createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)
		}

		jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 4, 1, 5*time.Minute)
		require.NoError(t, err)
		require.Len(t, jobs, 4, "claim must fill the whole capacity")

		fast, slow := countByLane(jobs)
		assert.Equal(t, 3, fast, "every due fast job must be claimed")
		assert.Equal(t, 1, slow, "slow claims must not exceed the slow budget")

		// Drain for the next subtest.
		for _, j := range jobs {
			require.NoError(t, svc.ReleaseLease(ctx, j.UID, worker.UID, now.Add(2*time.Hour)))
		}
		remaining, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 100, 100, 5*time.Minute)
		require.NoError(t, err)
		for _, j := range remaining {
			require.NoError(t, svc.ReleaseLease(ctx, j.UID, worker.UID, now.Add(2*time.Hour)))
		}
	})

	t.Run("ZeroSlowBudgetClaimsNoSlow", func(t *testing.T) { //nolint:paralleltest // shares DB
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		slowJob := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)
		markSlow(t, ctx, dbSvc, slowJob.UID)
		fastJob := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)

		jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 4, 0, 5*time.Minute)
		require.NoError(t, err)
		require.Len(t, jobs, 1, "only the fast job is claimable with a zero slow budget")
		assert.Equal(t, fastJob.UID, jobs[0].UID)

		// Drain.
		require.NoError(t, svc.ReleaseLease(ctx, jobs[0].UID, worker.UID, now.Add(2*time.Hour)))
		rest, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 100, 100, 5*time.Minute)
		require.NoError(t, err)
		for _, j := range rest {
			require.NoError(t, svc.ReleaseLease(ctx, j.UID, worker.UID, now.Add(2*time.Hour)))
		}
	})

	t.Run("IdleFastLaneLetsSlowBorrow", func(t *testing.T) { //nolint:paralleltest // shares DB
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// No fast work due: slow may borrow up to the budget.
		for i := 0; i < 5; i++ {
			j := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)
			markSlow(t, ctx, dbSvc, j.UID)
		}

		jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 4, 3, 5*time.Minute)
		require.NoError(t, err)
		require.Len(t, jobs, 3, "with no fast work, slow claims exactly the budget")
		_, slow := countByLane(jobs)
		assert.Equal(t, 3, slow)

		// Drain.
		for _, j := range jobs {
			require.NoError(t, svc.ReleaseLease(ctx, j.UID, worker.UID, now.Add(2*time.Hour)))
		}
		rest, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 100, 100, 5*time.Minute)
		require.NoError(t, err)
		for _, j := range rest {
			require.NoError(t, svc.ReleaseLease(ctx, j.UID, worker.UID, now.Add(2*time.Hour)))
		}
	})

	t.Run("FastBoundedByRemainingCapacity", func(t *testing.T) { //nolint:paralleltest // shares DB
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// 3 fast + 3 slow due, capacity 4, generous slow budget 4: slow takes
		// its share first (all 3 due), fast fills the one remaining slot. The
		// total never exceeds the capacity.
		for i := 0; i < 3; i++ {
			createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)
			j := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)
			markSlow(t, ctx, dbSvc, j.UID)
		}

		jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 4, 4, 5*time.Minute)
		require.NoError(t, err)
		require.Len(t, jobs, 4)
		fast, slow := countByLane(jobs)
		assert.Equal(t, 1, fast, "fast fills the capacity slow left over")
		assert.Equal(t, 3, slow)

		// Drain for the next subtest.
		for _, j := range jobs {
			require.NoError(t, svc.ReleaseLease(ctx, j.UID, worker.UID, now.Add(2*time.Hour)))
		}
		rest, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 100, 100, 5*time.Minute)
		require.NoError(t, err)
		for _, j := range rest {
			require.NoError(t, svc.ReleaseLease(ctx, j.UID, worker.UID, now.Add(2*time.Hour)))
		}
	})

	t.Run("SaturatedFastLaneCannotStarveSlow", func(t *testing.T) { //nolint:paralleltest // shares DB
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// Regression for the live starvation (2026-07-02, dev instance): more
		// eligible fast jobs than free capacity — the permanent state of any
		// fleet whose fast periods fit inside the claim-ahead window. The old
		// fast-first claim filled every slot with fast work and computed the
		// slow allowance from the leftovers (always zero), so the slow lane
		// never claimed a single job. Slow must get its budgeted share.
		for i := 0; i < 6; i++ {
			createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)
		}
		for i := 0; i < 3; i++ {
			j := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)
			markSlow(t, ctx, dbSvc, j.UID)
		}

		jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 4, 2, 5*time.Minute)
		require.NoError(t, err)
		require.Len(t, jobs, 4, "claim must still fill the whole capacity")

		fast, slow := countByLane(jobs)
		assert.Equal(t, 2, slow, "slow claims its full budget even under fast saturation")
		assert.Equal(t, 2, fast, "fast fills the remainder")
	})
}

// TestClaimOrdersByEffectiveWithinLane verifies D1: WFQ ordering by
// effective_scheduled_at still applies within each lane after the split.
//
//nolint:paralleltest // Test shares database state
func TestClaimOrdersByEffectiveWithinLane(t *testing.T) {
	dbSvc, ctx := setupTestDB(t)
	defer func() { _ = dbSvc.Close() }()

	svc := checkjobsvc.NewService(dbSvc.DB())
	org := createTestOrg(t, ctx, dbSvc)
	worker := createTestWorker(t, ctx, dbSvc, nil)

	now := time.Now()

	// Two due slow jobs with distinct effective deadlines.
	slowLate := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-1*time.Second), nil)
	markSlow(t, ctx, dbSvc, slowLate.UID)
	setEffective(t, ctx, dbSvc, slowLate.UID, now.Add(30*time.Second))
	slowEarly := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-1*time.Second), nil)
	markSlow(t, ctx, dbSvc, slowEarly.UID)
	setEffective(t, ctx, dbSvc, slowEarly.UID, now.Add(-10*time.Second))

	// One slow slot: the earlier effective deadline wins within the lane.
	jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 1, 1, 5*time.Minute)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, slowEarly.UID, jobs[0].UID,
		"within the slow lane, the earlier effective_scheduled_at must be claimed first")
}

//nolint:paralleltest // Test shares database state
func TestClaimJobsForCheck(t *testing.T) {
	dbSvc, ctx := setupTestDB(t)
	defer func() { _ = dbSvc.Close() }()

	svc := checkjobsvc.NewService(dbSvc.DB())
	org := createTestOrg(t, ctx, dbSvc)

	t.Run("ClaimsTargetedJob", func(t *testing.T) { //nolint:paralleltest // Test shares database state
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		target := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-1*time.Second), nil)
		other := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-1*time.Second), nil)

		jobs, err := svc.ClaimJobsForCheck(ctx, worker.UID, nil, target.CheckUID)
		require.NoError(t, err)
		require.Len(t, jobs, 1, "should claim only the targeted check's job")
		assert.Equal(t, target.UID, jobs[0].UID)
		assert.Equal(t, worker.UID, *jobs[0].LeaseWorkerUID)

		// The other job is untouched
		var otherDB models.CheckJob
		err = dbSvc.DB().NewSelect().Model(&otherDB).Where("uid = ?", other.UID).Scan(ctx)
		require.NoError(t, err)
		assert.Nil(t, otherDB.LeaseWorkerUID, "non-targeted job should not be claimed")
	})

	t.Run("RegionMismatchReturnsNothing", func(t *testing.T) { //nolint:paralleltest // Test shares database state
		now := time.Now()
		euRegion := "eu-west-1"
		usRegion := "us-east-1"

		// US-region check; EU worker should not claim it.
		usJob := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-1*time.Second), &usRegion)
		euWorker := createTestWorker(t, ctx, dbSvc, &euRegion)

		jobs, err := svc.ClaimJobsForCheck(ctx, euWorker.UID, &euRegion, usJob.CheckUID)
		require.NoError(t, err)
		assert.Empty(t, jobs, "EU worker should not claim US check")
	})

	t.Run("AlreadyClaimedReturnsNothing", func(t *testing.T) { //nolint:paralleltest // Test shares database state
		worker := createTestWorker(t, ctx, dbSvc, nil)
		other := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-1*time.Second), nil)
		// Pre-claim by another worker with a future lease.
		_, err := dbSvc.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("lease_worker_uid = ?", other.UID).
			Set("lease_expires_at = ?", now.Add(60*time.Second)).
			Where("uid = ?", job.UID).
			Exec(ctx)
		require.NoError(t, err)

		jobs, err := svc.ClaimJobsForCheck(ctx, worker.UID, nil, job.CheckUID)
		require.NoError(t, err)
		assert.Empty(t, jobs, "should not claim a job leased by another worker")
	})

	t.Run("ReclaimsOnExpiredLease", func(t *testing.T) { //nolint:paralleltest // Test shares database state
		worker := createTestWorker(t, ctx, dbSvc, nil)
		other := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-30*time.Second), nil)
		_, err := dbSvc.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("lease_worker_uid = ?", other.UID).
			Set("lease_expires_at = ?", now.Add(-10*time.Second)).
			Set("lease_starts = ?", 1).
			Where("uid = ?", job.UID).
			Exec(ctx)
		require.NoError(t, err)

		jobs, err := svc.ClaimJobsForCheck(ctx, worker.UID, nil, job.CheckUID)
		require.NoError(t, err)
		require.Len(t, jobs, 1, "should re-claim a job whose lease expired")
		assert.Equal(t, worker.UID, *jobs[0].LeaseWorkerUID)
		assert.Equal(t, 2, jobs[0].LeaseStarts)
	})

	t.Run("UnknownCheckReturnsNothing", func(t *testing.T) { //nolint:paralleltest // Test shares database state
		worker := createTestWorker(t, ctx, dbSvc, nil)
		jobs, err := svc.ClaimJobsForCheck(ctx, worker.UID, nil, uuid.NewString())
		require.NoError(t, err)
		assert.Empty(t, jobs)
	})

	t.Run("AttachesCheckAtClaimTime", func(t *testing.T) { //nolint:paralleltest // Test shares database state
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		target := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-1*time.Second), nil)

		jobs, err := svc.ClaimJobsForCheck(ctx, worker.UID, nil, target.CheckUID)
		require.NoError(t, err)
		require.Len(t, jobs, 1)
		require.NotNil(t, jobs[0].Check, "express-claimed job must have its check attached")
		assert.Equal(t, target.CheckUID, jobs[0].Check.UID)
	})
}

//nolint:paralleltest // Test shares database state
func TestReleaseLease(t *testing.T) {
	dbSvc, ctx := setupTestDB(t)
	defer func() { _ = dbSvc.Close() }()

	svc := checkjobsvc.NewService(dbSvc.DB())
	org := createTestOrg(t, ctx, dbSvc)

	t.Run("ReleaseAndReschedule", func(t *testing.T) { //nolint:paralleltest // Test shares database state
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)

		// Claim the job
		leaseExpiry := now.Add(60 * time.Second)
		_, err := dbSvc.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("lease_worker_uid = ?", worker.UID).
			Set("lease_expires_at = ?", leaseExpiry).
			Set("lease_starts = ?", 1).
			Where("uid = ?", job.UID).
			Exec(ctx)
		require.NoError(t, err)

		// Release lease with new scheduled time
		nextScheduled := now.Add(5 * time.Minute)
		err = svc.ReleaseLease(ctx, job.UID, worker.UID, nextScheduled)
		require.NoError(t, err)

		// Verify lease was released
		var dbJob models.CheckJob
		err = dbSvc.DB().NewSelect().Model(&dbJob).Where("uid = ?", job.UID).Scan(ctx)
		require.NoError(t, err)

		assert.Nil(t, dbJob.LeaseWorkerUID, "lease_worker_uid should be NULL")
		assert.Nil(t, dbJob.LeaseExpiresAt, "lease_expires_at should be NULL")
		assert.Equal(t, 0, dbJob.LeaseStarts, "lease_starts should be reset to 0")
		assert.WithinDuration(t, nextScheduled, *dbJob.ScheduledAt, 1*time.Second)
	})

	t.Run("OnlyReleaseOwnLease", func(t *testing.T) { //nolint:paralleltest // Test shares database state
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)
		otherWorker := createTestWorker(t, ctx, dbSvc, nil)

		// Claim job with different worker
		leaseExpiry := now.Add(60 * time.Second)
		_, err := dbSvc.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("lease_worker_uid = ?", otherWorker.UID).
			Set("lease_expires_at = ?", leaseExpiry).
			Where("uid = ?", job.UID).
			Exec(ctx)
		require.NoError(t, err)

		// Try to release with wrong worker
		nextScheduled := now.Add(5 * time.Minute)
		err = svc.ReleaseLease(ctx, job.UID, worker.UID, nextScheduled)
		require.Error(t, err, "should fail when trying to release another worker's lease")
	})

	t.Run("ReleaseReAnchorsEffectiveToNewSchedule", func(t *testing.T) { //nolint:paralleltest // shares DB
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)

		leaseExpiry := now.Add(60 * time.Second)
		_, err := dbSvc.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("lease_worker_uid = ?", worker.UID).
			Set("lease_expires_at = ?", leaseExpiry).
			Where("uid = ?", job.UID).
			Exec(ctx)
		require.NoError(t, err)

		nextScheduled := now.Add(5 * time.Minute)
		require.NoError(t, svc.ReleaseLease(ctx, job.UID, worker.UID, nextScheduled))

		var dbJob models.CheckJob
		require.NoError(t, dbSvc.DB().NewSelect().Model(&dbJob).Where("uid = ?", job.UID).Scan(ctx))
		require.NotNil(t, dbJob.EffectiveScheduledAt)
		assert.WithinDuration(t, nextScheduled, *dbJob.EffectiveScheduledAt, 1*time.Second,
			"plain release re-anchors effective_scheduled_at to the new schedule")
	})

	t.Run("ReleaseWithSchedulingStatePersistsCostAndEffective", func(t *testing.T) { //nolint:paralleltest // shares DB
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)

		leaseExpiry := now.Add(60 * time.Second)
		_, err := dbSvc.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("lease_worker_uid = ?", worker.UID).
			Set("lease_expires_at = ?", leaseExpiry).
			Where("uid = ?", job.UID).
			Exec(ctx)
		require.NoError(t, err)

		nextScheduled := now.Add(1 * time.Minute)
		effective := now.Add(80 * time.Second) // penalized past the schedule
		require.NoError(t, svc.ReleaseLeaseWithSchedulingState(
			ctx, job.UID, worker.UID, nextScheduled, 1234.5, 678.0, effective, scheduling.LaneSlow,
		))

		var dbJob models.CheckJob
		require.NoError(t, dbSvc.DB().NewSelect().Model(&dbJob).Where("uid = ?", job.UID).Scan(ctx))
		assert.InDelta(t, 1234.5, dbJob.CostEWMAMs, 0.001, "cost EWMA must be persisted")
		assert.InDelta(t, 678.0, dbJob.DelayEWMAMs, 0.001, "delay EWMA must be persisted")
		require.NotNil(t, dbJob.EffectiveScheduledAt)
		assert.WithinDuration(t, effective, *dbJob.EffectiveScheduledAt, 1*time.Second,
			"effective_scheduled_at must be persisted from the cost-folding release")
		assert.WithinDuration(t, nextScheduled, *dbJob.ScheduledAt, 1*time.Second)
		assert.Nil(t, dbJob.LeaseWorkerUID, "lease must be released")
		assert.Equal(t, scheduling.LaneSlow, dbJob.Lane,
			"the classified lane must be persisted by the post-exec release")
	})

	t.Run("PlainReleaseLeavesLaneUnchanged", func(t *testing.T) { //nolint:paralleltest // shares DB
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)
		markSlow(t, ctx, dbSvc, job.UID)

		leaseExpiry := now.Add(60 * time.Second)
		_, err := dbSvc.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("lease_worker_uid = ?", worker.UID).
			Set("lease_expires_at = ?", leaseExpiry).
			Where("uid = ?", job.UID).
			Exec(ctx)
		require.NoError(t, err)

		// The no-sample release (deferral / reaper / remote) must not touch the
		// lane: there is no fresh cost signal to reclassify from.
		require.NoError(t, svc.ReleaseLease(ctx, job.UID, worker.UID, now.Add(time.Minute)))

		var dbJob models.CheckJob
		require.NoError(t, dbSvc.DB().NewSelect().Model(&dbJob).Where("uid = ?", job.UID).Scan(ctx))
		assert.Equal(t, scheduling.LaneSlow, dbJob.Lane,
			"ReleaseLease (no cost sample) must leave the lane unchanged")
	})
}

// setTestCheckJobPeriod overwrites a test job's period column directly —
// createTestCheckJob always creates the underlying check via models.NewCheck,
// which defaults Period to 1 minute, so tests needing a different period patch
// it in after creation the same way the helper already patches
// scheduled_at/region.
//
//nolint:revive // Test helper function, context parameter order is acceptable
func setTestCheckJobPeriod(
	t *testing.T, ctx context.Context, svc *sqlite.Service, jobUID string, period time.Duration,
) {
	t.Helper()

	_, err := svc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("period = ?", timeutils.Duration(period)).
		Where("uid = ?", jobUID).
		Exec(ctx)
	require.NoError(t, err, "failed to set test check job period")
}

// TestClaimJobsBoundedClaimAheadWindow covers D3 (spec 2026-07-05-08): the
// per-job eligibility window is clamped to min(FetchMaxAhead, period/2, 30s)
// instead of the flat FetchMaxAhead, so a short-period job isn't claimed (and
// left sleeping in-runner) minutes before it's actually due.
//
// Subtests share one DB (like the rest of this file) and each claims with a
// generous limit, so a subtest's own unclaimed job can still be sitting in
// the table when a later subtest runs with a looser maxAhead. Assertions
// therefore check membership of this subtest's own job UID rather than the
// exact claimed count, so they stay correct regardless of subtest order or
// carried-over rows.
//
//nolint:paralleltest // Test shares database state
func TestClaimJobsBoundedClaimAheadWindow(t *testing.T) {
	dbSvc, ctx := setupTestDB(t)
	defer func() { _ = dbSvc.Close() }()

	svc := checkjobsvc.NewService(dbSvc.DB())
	org := createTestOrg(t, ctx, dbSvc)

	containsUID := func(jobs []*models.CheckJob, uid string) bool {
		for _, j := range jobs {
			if j.UID == uid {
				return true
			}
		}

		return false
	}

	t.Run("ShortPeriodJobNotClaimedOutsideItsOwnWindow", func(t *testing.T) { //nolint:paralleltest // shares DB
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// 1-minute period -> clamp = min(FetchMaxAhead, 30s, 30s) = 30s.
		// scheduled_at 40s out is outside that 30s window, even though the
		// flat FetchMaxAhead (5 minutes) would have admitted it.
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(40*time.Second), nil)
		setTestCheckJobPeriod(t, ctx, dbSvc, job.UID, time.Minute)

		jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
		require.NoError(t, err)
		assert.False(t, containsUID(jobs, job.UID),
			"a 1-minute-period job 40s out must not be claimed (window clamps to 30s)")
	})

	t.Run("ShortPeriodJobClaimedInsideItsOwnWindow", func(t *testing.T) { //nolint:paralleltest // shares DB
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// Same 1-minute period, but scheduled_at only 10s out: inside the 30s
		// clamp window, so it must still be claimed (claim-ahead still works
		// for the common case, just bounded).
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(10*time.Second), nil)
		setTestCheckJobPeriod(t, ctx, dbSvc, job.UID, time.Minute)

		jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
		require.NoError(t, err)
		assert.True(t, containsUID(jobs, job.UID), "a 1-minute-period job 10s out is inside its own 30s window")
	})

	t.Run("LongPeriodJobClaimedWellWithinHalfPeriodBound", func(t *testing.T) { //nolint:paralleltest // shares DB
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// 10-minute period -> period/2 = 5 minutes, well above the 30s floor,
		// so the clamp for this job is min(FetchMaxAhead, 5m, 30s) = 30s (the
		// flat floor still applies) — scheduled_at 20s out is well inside it.
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(20*time.Second), nil)
		setTestCheckJobPeriod(t, ctx, dbSvc, job.UID, 10*time.Minute)

		jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
		require.NoError(t, err)
		assert.True(t, containsUID(jobs, job.UID), "a 10-minute-period job 20s out is inside the 30s floor")
	})

	t.Run("SmallFetchMaxAheadOverrideStillWins", func(t *testing.T) { //nolint:paralleltest // shares DB
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// An operator override tighter than the per-job clamp must still be
		// respected: FetchMaxAhead=5s beats the 1-minute-period job's own 30s
		// clamp, so a job 10s out is NOT claimed.
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(10*time.Second), nil)
		setTestCheckJobPeriod(t, ctx, dbSvc, job.UID, time.Minute)

		jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Second)
		require.NoError(t, err)
		assert.False(t, containsUID(jobs, job.UID), "a small FetchMaxAhead override must still be respected")
	})

	t.Run("DueNowJobAlwaysClaimedRegardlessOfPeriod", func(t *testing.T) { //nolint:paralleltest // shares DB
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// scheduled_at in the past: due now, must be claimed regardless of
		// the clamp (the clamp only bounds how far AHEAD a job may be
		// claimed, never how overdue one may be).
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-5*time.Second), nil)
		setTestCheckJobPeriod(t, ctx, dbSvc, job.UID, time.Minute)

		jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
		require.NoError(t, err)
		assert.True(t, containsUID(jobs, job.UID), "a due-now job must always be claimed regardless of period")
	})
}

// TestClaimJobs_LongPeriodLeaseExpiry proves (rather than assumes) that a
// long check period — e.g. domain's new 336h (2-week) interval option, spec
// 2026-08-15-07 — flows correctly through claim-time lease accounting:
//   - the job is still claimable when due, regardless of how long its period is
//   - lease_expires_at is computed as latest(scheduled_at, now) + period + 30s
//     (updateSingleJobLease, checkjobsvc/service.go) with NO cap at 24h — a
//     wrong implementation (e.g. one that silently clamped the lease window)
//     would fail this assertion.
//
//nolint:paralleltest // Test shares database state
func TestClaimJobs_LongPeriodLeaseExpiry(t *testing.T) {
	dbSvc, ctx := setupTestDB(t)
	defer func() { _ = dbSvc.Close() }()

	svc := checkjobsvc.NewService(dbSvc.DB())
	org := createTestOrg(t, ctx, dbSvc)
	worker := createTestWorker(t, ctx, dbSvc, nil)

	now := time.Now()
	longPeriod := 336 * time.Hour // 2 weeks

	// Due now (scheduled slightly in the past), long period.
	job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-5*time.Second), nil)
	setTestCheckJobPeriod(t, ctx, dbSvc, job.UID, longPeriod)

	jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
	require.NoError(t, err)

	var claimed *models.CheckJob

	for _, j := range jobs {
		if j.UID == job.UID {
			claimed = j
		}
	}

	require.NotNil(t, claimed, "a due-now job with a 2-week period must still be claimed")
	require.NotNil(t, claimed.LeaseExpiresAt)

	latest := *claimed.ScheduledAt
	if now.After(latest) {
		latest = now
	}

	expectedExpiry := latest.Add(longPeriod + 30*time.Second)
	assert.WithinDuration(t, expectedExpiry, *claimed.LeaseExpiresAt, 2*time.Second,
		"lease_expires_at must be scheduled_at/now + FULL period + 30s, not clamped to a shorter window")

	// Sanity bound proving the assertion above is actually exercising the
	// long-period path (not silently passing because it's ~= a short lease):
	// the lease must be valid for well over 24h.
	assert.Greater(t, claimed.LeaseExpiresAt.Sub(now), 24*time.Hour,
		"a 2-week-period job's lease must extend well beyond 24h")
}

// TestClaimJobsFleetScaleClampBoundsParkedCount is the aggregate/fleet-scale
// counterpart of TestClaimJobsBoundedClaimAheadWindow, modeling AC4 directly:
// with ~28 jobs on a short (3-minute, i.e. 1-minute base period split across
// 3 regions) period spread evenly across their whole cycle and default
// config (FetchMaxAhead=5m), only the jobs within their own 30s clamp window
// are claimed at once — not all 28. Before D3 the flat 5-minute
// FetchMaxAhead window would have admitted every one of them (they're all
// "due within 5 minutes" of a 3-minute period), which is exactly the ~22-of-25
// parked-runner behavior the spec's live incident observed.
//
//nolint:paralleltest // Test shares database state
func TestClaimJobsFleetScaleClampBoundsParkedCount(t *testing.T) {
	dbSvc, ctx := setupTestDB(t)
	defer func() { _ = dbSvc.Close() }()

	svc := checkjobsvc.NewService(dbSvc.DB())
	org := createTestOrg(t, ctx, dbSvc)
	worker := createTestWorker(t, ctx, dbSvc, nil)

	const (
		fleetSize  = 28
		jobPeriod  = 3 * time.Minute // 1-minute base period x 3 regions, matching the spec's incident
		fetchAhead = 5 * time.Minute // default config
	)

	now := time.Now()

	// Spread scheduled_at evenly across the whole 3-minute cycle, exactly
	// like a real leveled fleet (spec D1) would look between waves — most
	// jobs are NOT due imminently, only a poll-cadence-sized slice is.
	for i := range fleetSize {
		offset := time.Duration(i) * (jobPeriod / fleetSize)
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(offset), nil)
		setTestCheckJobPeriod(t, ctx, dbSvc, job.UID, jobPeriod)
	}

	jobs, _, err := svc.ClaimJobs(ctx, worker.UID, nil, fleetSize, fleetSize, fetchAhead)
	require.NoError(t, err)

	// jobPeriod/2 = 90s, which is above the 30s floor, so every job's own
	// clamp is min(5m, 90s, 30s) = 30s. Only jobs scheduled within the next
	// 30s should be claimed: roughly fleetSize * (30s / jobPeriod) ~= 4-5 of
	// 28, a small fraction — nowhere near the flat-window ~22-of-25 the
	// spec's incident observed.
	assert.NotEmpty(t, jobs, "at least the immediately-due jobs must still be claimed")
	assert.Less(t, len(jobs), fleetSize/2,
		"the bounded clamp must admit only a small fraction of the fleet, not nearly all of it")

	for _, j := range jobs {
		assert.True(t, j.ScheduledAt.Before(now.Add(30*time.Second+time.Second)),
			"every claimed job must be within its own 30s clamp window (with a 1s scheduling-jitter allowance)")
	}
}

// TestClaimJobsNextEligibleHint covers the claim's next-eligible hint (spec
// 2026-07-20-03). The fetcher's only periodic wake used to be a flat
// one-minute poll, so on an idle worker a 10s-period job — claimable only
// period/2 = 5s before it is due — was never claimable at the moment a
// completion woke the fetcher, and ran once per minute. The claim now
// reports how long until the earliest still-unleased job in scope becomes
// claimable; the fetcher sleeps exactly that long.
func TestClaimJobsNextEligibleHint(t *testing.T) {
	t.Parallel()

	t.Run("ShortPeriodJobYieldsItsEligibilityDelay", func(t *testing.T) {
		t.Parallel()

		dbSvc, ctx := setupTestDB(t)
		defer func() { _ = dbSvc.Close() }()

		svc := checkjobsvc.NewService(dbSvc.DB())
		org := createTestOrg(t, ctx, dbSvc)
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// 10s period, due 10s out: claimable in ~5s (scheduled_at − period/2).
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(10*time.Second), nil)
		setTestCheckJobPeriod(t, ctx, dbSvc, job.UID, 10*time.Second)

		jobs, nextIn, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
		require.NoError(t, err)
		assert.Empty(t, jobs, "the job is outside its own 5s claim window")
		assert.InDelta(t, (5 * time.Second).Seconds(), nextIn.Seconds(), 1.5,
			"the hint must be ~scheduled_at − period/2, not a flat fallback")
	})

	t.Run("ClaimedBatchProducesNoImmediateRepoll", func(t *testing.T) {
		t.Parallel()

		dbSvc, ctx := setupTestDB(t)
		defer func() { _ = dbSvc.Close() }()

		svc := checkjobsvc.NewService(dbSvc.DB())
		org := createTestOrg(t, ctx, dbSvc)
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// A due job is claimed and leased in this very call; its next tick is
		// only written at release, so nothing upcoming remains — the hint must
		// be zero, not an immediate wasted re-poll against our own claim.
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-time.Second), nil)
		setTestCheckJobPeriod(t, ctx, dbSvc, job.UID, 10*time.Second)

		jobs, nextIn, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
		require.NoError(t, err)
		require.Len(t, jobs, 1)
		assert.Zero(t, nextIn, "a job claimed in this batch must not drive the hint")
	})

	t.Run("ForeignLeaseAndFarFutureAreExcluded", func(t *testing.T) {
		t.Parallel()

		dbSvc, ctx := setupTestDB(t)
		defer func() { _ = dbSvc.Close() }()

		svc := checkjobsvc.NewService(dbSvc.DB())
		org := createTestOrg(t, ctx, dbSvc)
		worker := createTestWorker(t, ctx, dbSvc, nil)
		other := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// Upcoming but leased by another worker: that worker's release will
		// reschedule it, so it must not drive this worker's wake.
		leased := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(10*time.Second), nil)
		setTestCheckJobPeriod(t, ctx, dbSvc, leased.UID, 10*time.Second)
		_, err := dbSvc.DB().NewUpdate().
			Model((*models.CheckJob)(nil)).
			Set("lease_worker_uid = ?", other.UID).
			Set("lease_expires_at = ?", now.Add(2*time.Minute)).
			Where("uid = ?", leased.UID).
			Exec(ctx)
		require.NoError(t, err)

		// Beyond the hint horizon: the fallback poll reaches it first.
		far := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(3*time.Minute), nil)
		setTestCheckJobPeriod(t, ctx, dbSvc, far.UID, time.Hour)

		jobs, nextIn, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
		require.NoError(t, err)
		assert.Empty(t, jobs)
		assert.Zero(t, nextIn, "leased and beyond-horizon jobs must yield no hint")
	})

	t.Run("EligibleButUnclaimedJobFloorsTheHint", func(t *testing.T) {
		t.Parallel()

		dbSvc, ctx := setupTestDB(t)
		defer func() { _ = dbSvc.Close() }()

		svc := checkjobsvc.NewService(dbSvc.DB())
		org := createTestOrg(t, ctx, dbSvc)
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// Already inside its own 30s window (so eligible right now) but the
		// claim has zero capacity: the hint must clamp to a small positive
		// delay — never zero/negative, which would fall back to the flat poll
		// or spin the fetcher.
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(5*time.Second), nil)
		setTestCheckJobPeriod(t, ctx, dbSvc, job.UID, time.Minute)

		jobs, nextIn, err := svc.ClaimJobs(ctx, worker.UID, nil, 0, 0, 5*time.Minute)
		require.NoError(t, err)
		assert.Empty(t, jobs)
		assert.Positive(t, nextIn)
		assert.LessOrEqual(t, nextIn, time.Second,
			"an already-eligible job must yield a floored short hint")
	})

	t.Run("AgentHintIsScopedToOrgAndRegion", func(t *testing.T) {
		t.Parallel()

		dbSvc, ctx := setupTestDB(t)
		defer func() { _ = dbSvc.Close() }()

		svc := checkjobsvc.NewService(dbSvc.DB())
		org := createTestOrg(t, ctx, dbSvc)
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		region := "@test-org/dc1"
		job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(10*time.Second), &region)
		setTestCheckJobPeriod(t, ctx, dbSvc, job.UID, 10*time.Second)

		jobs, nextIn, err := svc.ClaimJobsForAgent(
			ctx, worker.UID, checkjobsvc.AgentScope{OrgUID: org.UID, Region: region}, "", 10, 5*time.Minute,
		)
		require.NoError(t, err)
		assert.Empty(t, jobs, "the job is outside its own 5s claim window")
		assert.InDelta(t, (5 * time.Second).Seconds(), nextIn.Seconds(), 1.5,
			"the agent claim must carry the same eligibility hint")

		// The same claim for a different private region must see nothing —
		// the hint is bounded by the agent's hard scope like the claim itself.
		jobs, nextIn, err = svc.ClaimJobsForAgent(
			ctx, worker.UID,
			checkjobsvc.AgentScope{OrgUID: org.UID, Region: "@test-org/dc2"}, "", 10, 5*time.Minute,
		)
		require.NoError(t, err)
		assert.Empty(t, jobs)
		assert.Zero(t, nextIn, "another region's job must not leak into the hint")
	})
}

// TestClaimJobsForAgentRejectsInvalidScope is the fail-closed guard on the
// agent claim scope. Dropping the organization predicate is what lets a
// platform agent serve a shared cloud region across tenants, so every scope
// that is not one of the two legitimate shapes must be REFUSED rather than
// silently widened:
//
//   - no region at all — a scope with no region predicate would claim
//     everything;
//   - System with an OrgUID — an ambiguous half-widened scope;
//   - System pointed at an `@org/region` private slug — a cross-tenant read of
//     somebody's private location;
//   - a non-system scope with no OrgUID — an org agent claiming every org.
//
// The claim must fail with ErrInvalidAgentScope, never return rows.
func TestClaimJobsForAgentRejectsInvalidScope(t *testing.T) {
	t.Parallel()

	dbSvc, ctx := setupTestDB(t)
	// Cleanup rather than defer: the subtests below are parallel, so they run
	// after this function returns but before registered cleanups.
	t.Cleanup(func() { _ = dbSvc.Close() })

	svc := checkjobsvc.NewService(dbSvc.DB())
	org := createTestOrg(t, ctx, dbSvc)
	worker := createTestWorker(t, ctx, dbSvc, nil)

	// A due job in a shared cloud region, so a widened scope would have
	// something to leak: the rejections below are not vacuous.
	cloudRegion := "eu-west-1"
	createTestCheckJob(t, ctx, dbSvc, org.UID, time.Now().Add(-time.Minute), &cloudRegion)

	invalid := map[string]checkjobsvc.AgentScope{
		"empty region, org agent":                  {OrgUID: org.UID},
		"empty region, system agent":               {System: true},
		"empty everything":                         {},
		"system agent carrying an org":             {OrgUID: org.UID, Region: cloudRegion, System: true},
		"system agent on a private region":         {Region: "@test-org/dc1", System: true},
		"org agent without an org":                 {Region: cloudRegion},
		"org agent without an org, private region": {Region: "@test-org/dc1"},
	}

	for name, scope := range invalid {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			jobs, nextIn, err := svc.ClaimJobsForAgent(ctx, worker.UID, scope, "", 10, 5*time.Minute)
			require.ErrorIs(t, err, checkjobsvc.ErrInvalidAgentScope,
				"an illegitimate scope must fail closed, never be widened")
			assert.Empty(t, jobs, "a rejected scope must never return rows")
			assert.Zero(t, nextIn, "a rejected scope must not leak an eligibility hint either")
		})
	}

	// Positive control: the very same job IS claimable by a well-formed system
	// scope, which proves the rejections above are about the scope shape and
	// not about the fixture.
	t.Run("valid system scope claims it", func(t *testing.T) {
		t.Parallel()

		jobs, _, err := svc.ClaimJobsForAgent(
			ctx, worker.UID, checkjobsvc.AgentScope{Region: cloudRegion, System: true}, "", 10, 5*time.Minute,
		)
		require.NoError(t, err)
		assert.Len(t, jobs, 1, "a well-formed system scope claims the shared region's job")
	})
}
