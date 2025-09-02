package checkjobsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
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

	// Update the job with test-specific values
	_, err = svc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", scheduledAt).
		Set("region = ?", region).
		Where("uid = ?", job.UID).
		Exec(ctx)
	require.NoError(t, err, "failed to update check job")

	// Refresh the job to get updated values
	job.ScheduledAt = &scheduledAt
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
		jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 5*time.Minute)
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
		jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 3, 5*time.Minute)
		require.NoError(t, err)
		assert.Len(t, jobs, 3, "should respect limit")
	})

	t.Run("SkipJobsWithActiveLease", func(t *testing.T) { //nolint:paralleltest // Test shares database state
		worker := createTestWorker(t, ctx, dbSvc, nil)
		now := time.Now()

		// First, claim and clear any existing unclaimed jobs to avoid interference
		existingJobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 100, 5*time.Minute)
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
		jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 5*time.Minute)
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
		jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 5*time.Minute)
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
		jobs, err := svc.ClaimJobs(ctx, euWorker.UID, &euRegion, 10, 5*time.Minute)
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
		existingJobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 100, 10*time.Minute)
		require.NoError(t, err)
		// Mark them all as released with far future schedule so they don't interfere
		for _, existingJob := range existingJobs {
			_ = svc.ReleaseLease(ctx, existingJob.UID, worker.UID, now.Add(2*time.Hour))
		}

		// Create job far in the future (beyond max_ahead window)
		_ = createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(10*time.Minute), nil)

		// Try to claim with 5 minute max_ahead - should not claim the far future job
		jobs, err := svc.ClaimJobs(ctx, worker.UID, nil, 10, 5*time.Minute)
		require.NoError(t, err)
		assert.Empty(t, jobs, "should not claim jobs beyond max_ahead")

		// Create job that is due now (within window)
		nearJob := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-10*time.Second), nil)

		// Should claim the near job
		jobs, err = svc.ClaimJobs(ctx, worker.UID, nil, 10, 5*time.Minute)
		require.NoError(t, err)
		require.Len(t, jobs, 1, "should claim job that is due")
		assert.Equal(t, nearJob.UID, jobs[0].UID)
	})

	t.Run("NoJobsAvailable", func(t *testing.T) {
		// Claim when no jobs exist
		newWorker := createTestWorker(t, ctx, dbSvc, nil)
		jobs, err := svc.ClaimJobs(ctx, newWorker.UID, nil, 10, 5*time.Minute)
		require.NoError(t, err)
		assert.Empty(t, jobs, "should return empty slice when no jobs available")
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
}
