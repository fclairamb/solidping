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
		jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
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
		jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 3, 3, 5*time.Minute)
		require.NoError(t, err)
		assert.Len(t, jobs, 3, "should respect limit")
	})

	t.Run("SkipJobsWithActiveLease", func(t *testing.T) { //nolint:paralleltest // Test shares database state
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// First, claim and clear any existing unclaimed jobs to avoid interference
		existingJobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 100, 100, 5*time.Minute)
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
		jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
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
		jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
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
		jobs, err := svc.ClaimJobs(ctx, euWorker.UID, &euRegion, 10, 10, 5*time.Minute)
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
		existingJobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 100, 100, 10*time.Minute)
		require.NoError(t, err)
		// Mark them all as released with far future schedule so they don't interfere
		for _, existingJob := range existingJobs {
			_ = svc.ReleaseLease(ctx, existingJob.UID, worker.UID, now.Add(2*time.Hour))
		}

		// Create job far in the future (beyond max_ahead window)
		_ = createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(10*time.Minute), nil)

		// Try to claim with 5 minute max_ahead - should not claim the far future job
		jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
		require.NoError(t, err)
		assert.Empty(t, jobs, "should not claim jobs beyond max_ahead")

		// Create job that is due now (within window)
		nearJob := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)

		// Should claim the near job
		jobs, err = svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
		require.NoError(t, err)
		require.Len(t, jobs, 1, "should claim job that is due")
		assert.Equal(t, nearJob.UID, jobs[0].UID)
	})

	t.Run("NoJobsAvailable", func(t *testing.T) {
		// Claim when no jobs exist
		newWorker := createTestWorker(t, ctx, dbSvc, nil)
		jobs, err := svc.ClaimJobs(ctx, newWorker.UID, nil, 10, 10, 5*time.Minute)
		require.NoError(t, err)
		assert.Empty(t, jobs, "should return empty slice when no jobs available")
	})

	t.Run("AttachesCheckAtClaimTime", func(t *testing.T) { //nolint:paralleltest // Test shares database state
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)
		createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-9*time.Second), nil)

		jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, 5*time.Minute)
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
	jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 1, 1, 5*time.Minute)
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

	jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 10, maxAhead)
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
// 2026-07-01-03 D3/D6): fast jobs may fill every slot, slow jobs are capped by
// slowLimit and by the capacity left after the fast claim, and an idle fast
// stream lets slow borrow the full budget.
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

		jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 4, 1, 5*time.Minute)
		require.NoError(t, err)
		require.Len(t, jobs, 4, "claim must fill the whole capacity")

		fast, slow := countByLane(jobs)
		assert.Equal(t, 3, fast, "every due fast job must be claimed")
		assert.Equal(t, 1, slow, "slow claims must not exceed the slow budget")

		// Drain for the next subtest.
		for _, j := range jobs {
			require.NoError(t, svc.ReleaseLease(ctx, j.UID, worker.UID, now.Add(2*time.Hour)))
		}
		remaining, err := svc.ClaimJobs(ctx, worker.UID, nil, 100, 100, 5*time.Minute)
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

		jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 4, 0, 5*time.Minute)
		require.NoError(t, err)
		require.Len(t, jobs, 1, "only the fast job is claimable with a zero slow budget")
		assert.Equal(t, fastJob.UID, jobs[0].UID)

		// Drain.
		require.NoError(t, svc.ReleaseLease(ctx, jobs[0].UID, worker.UID, now.Add(2*time.Hour)))
		rest, err := svc.ClaimJobs(ctx, worker.UID, nil, 100, 100, 5*time.Minute)
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

		jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 4, 3, 5*time.Minute)
		require.NoError(t, err)
		require.Len(t, jobs, 3, "with no fast work, slow claims exactly the budget")
		_, slow := countByLane(jobs)
		assert.Equal(t, 3, slow)

		// Drain.
		for _, j := range jobs {
			require.NoError(t, svc.ReleaseLease(ctx, j.UID, worker.UID, now.Add(2*time.Hour)))
		}
		rest, err := svc.ClaimJobs(ctx, worker.UID, nil, 100, 100, 5*time.Minute)
		require.NoError(t, err)
		for _, j := range rest {
			require.NoError(t, svc.ReleaseLease(ctx, j.UID, worker.UID, now.Add(2*time.Hour)))
		}
	})

	t.Run("SlowBoundedByRemainingCapacity", func(t *testing.T) { //nolint:paralleltest // shares DB
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// 3 fast + 3 slow due, capacity 4, generous slow budget 4: the fast
		// claim eats 3 slots, so slow gets min(4, 4−3) = 1.
		for i := 0; i < 3; i++ {
			createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)
			j := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)
			markSlow(t, ctx, dbSvc, j.UID)
		}

		jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 4, 4, 5*time.Minute)
		require.NoError(t, err)
		require.Len(t, jobs, 4)
		fast, slow := countByLane(jobs)
		assert.Equal(t, 3, fast)
		assert.Equal(t, 1, slow, "slow is bounded by the capacity left after the fast claim")
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
	jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 1, 1, 5*time.Minute)
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
