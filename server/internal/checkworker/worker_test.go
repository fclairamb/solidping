package checkworker

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/notifier"
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

//nolint:paralleltest // Subtests share runner and time reference
func TestCalculateNextScheduledAt(t *testing.T) {
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
	runner.worker = worker

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
func TestLastForStatus(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	// Create organization and worker
	org := models.NewOrganization("test-org", "")
	err := dbSvc.CreateOrganization(ctx, org)
	require.NoError(t, err)

	worker := models.NewWorker("test-worker", "Test Worker")
	_, err = dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.worker = worker

	// Create a check
	check := models.NewCheck(org.UID, "test-check", "http")
	err = dbSvc.CreateCheck(ctx, check)
	require.NoError(t, err)

	// Get the check job
	checkJob := new(models.CheckJob)
	err = dbSvc.DB().NewSelect().
		Model(checkJob).
		Where("check_uid = ?", check.UID).
		Scan(ctx)
	require.NoError(t, err)

	// Helper to get all results for this check (excluding initial status=0 result)
	getResults := func() []*models.Result {
		var results []*models.Result
		err := dbSvc.DB().NewSelect().
			Model(&results).
			Where("check_uid = ?", check.UID).
			Where("status != ?", int(models.ResultStatusCreated)).
			Order("created_at ASC").
			Scan(ctx)
		require.NoError(t, err)
		return results
	}

	// Helper to get results with last_for_status = true (excluding initial status=0 result)
	getLastForStatusResults := func() []*models.Result {
		var results []*models.Result
		err := dbSvc.DB().NewSelect().
			Model(&results).
			Where("check_uid = ?", check.UID).
			Where("last_for_status = ?", true).
			Where("status != ?", int(models.ResultStatusCreated)).
			Order("created_at ASC").
			Scan(ctx)
		require.NoError(t, err)
		return results
	}

	t.Run("FirstResultHasLastForStatus", func(t *testing.T) {
		// Insert first result with status up
		resultUID1, _ := uuid.NewV7()
		status1 := int(models.ResultStatusUp)
		duration1 := float32(100.0)
		lastForStatus := true
		result1 := models.Result{
			UID:             resultUID1.String(),
			OrganizationUID: org.UID,
			CheckUID:        check.UID,
			PeriodType:      "raw",
			PeriodStart:     time.Now(),
			WorkerUID:       &worker.UID,
			Status:          &status1,
			Duration:        &duration1,
			Metrics:         make(models.JSONMap),
			Output:          make(models.JSONMap),
			CreatedAt:       time.Now(),
			LastForStatus:   &lastForStatus,
		}

		err := dbSvc.DB().RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			// Clear previous last_for_status
			_, err := tx.NewUpdate().
				Model((*models.Result)(nil)).
				Set("last_for_status = NULL").
				Where("check_uid = ?", check.UID).
				Where("status = ?", status1).
				Where("last_for_status = true").
				Exec(ctx)
			if err != nil {
				return err
			}

			// Insert new result
			_, err = tx.NewInsert().Model(&result1).Exec(ctx)
			return err
		})
		require.NoError(t, err)

		// Verify result has last_for_status = true
		results := getResults()
		require.Len(t, results, 1)
		require.NotNil(t, results[0].LastForStatus)
		assert.True(t, *results[0].LastForStatus)

		lastResults := getLastForStatusResults()
		require.Len(t, lastResults, 1)
		assert.Equal(t, result1.UID, lastResults[0].UID)
	})

	t.Run("SecondResultWithSameStatusClearsPrevious", func(t *testing.T) {
		time.Sleep(10 * time.Millisecond) // Ensure different created_at

		// Insert second result with same status up
		resultUID2, _ := uuid.NewV7()
		status2 := int(models.ResultStatusUp)
		duration2 := float32(150.0)
		lastForStatus := true
		result2 := models.Result{
			UID:             resultUID2.String(),
			OrganizationUID: org.UID,
			CheckUID:        check.UID,
			PeriodType:      "raw",
			PeriodStart:     time.Now(),
			WorkerUID:       &worker.UID,
			Status:          &status2,
			Duration:        &duration2,
			Metrics:         make(models.JSONMap),
			Output:          make(models.JSONMap),
			CreatedAt:       time.Now(),
			LastForStatus:   &lastForStatus,
		}

		err := dbSvc.DB().RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			// Clear previous last_for_status
			_, err := tx.NewUpdate().
				Model((*models.Result)(nil)).
				Set("last_for_status = NULL").
				Where("check_uid = ?", check.UID).
				Where("status = ?", status2).
				Where("last_for_status = true").
				Exec(ctx)
			if err != nil {
				return err
			}

			// Insert new result
			_, err = tx.NewInsert().Model(&result2).Exec(ctx)
			return err
		})
		require.NoError(t, err)

		// Verify we have 2 results total
		results := getResults()
		require.Len(t, results, 2)

		// Verify only the second result has last_for_status = true
		lastResults := getLastForStatusResults()
		require.Len(t, lastResults, 1, "only one result should have last_for_status=true for status up")
		assert.Equal(t, result2.UID, lastResults[0].UID)

		// Verify first result no longer has last_for_status = true
		firstResult := new(models.Result)
		err = dbSvc.DB().NewSelect().
			Model(firstResult).
			Where("uid = ?", results[0].UID).
			Scan(ctx)
		require.NoError(t, err)
		assert.Nil(t, firstResult.LastForStatus, "first result should have last_for_status=NULL")
	})

	t.Run("DifferentStatusCanHaveOwnLastForStatus", func(t *testing.T) {
		time.Sleep(10 * time.Millisecond) // Ensure different created_at

		// Insert result with status down
		resultUID3, _ := uuid.NewV7()
		status3 := int(models.ResultStatusDown)
		duration3 := float32(0.0)
		lastForStatus := true
		result3 := models.Result{
			UID:             resultUID3.String(),
			OrganizationUID: org.UID,
			CheckUID:        check.UID,
			PeriodType:      "raw",
			PeriodStart:     time.Now(),
			WorkerUID:       &worker.UID,
			Status:          &status3,
			Duration:        &duration3,
			Metrics:         make(models.JSONMap),
			Output:          make(models.JSONMap),
			CreatedAt:       time.Now(),
			LastForStatus:   &lastForStatus,
		}

		err := dbSvc.DB().RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			// Clear previous last_for_status for status down
			_, err := tx.NewUpdate().
				Model((*models.Result)(nil)).
				Set("last_for_status = NULL").
				Where("check_uid = ?", check.UID).
				Where("status = ?", status3).
				Where("last_for_status = true").
				Exec(ctx)
			if err != nil {
				return err
			}

			// Insert new result
			_, err = tx.NewInsert().Model(&result3).Exec(ctx)
			return err
		})
		require.NoError(t, err)

		// Verify we have 3 results total
		results := getResults()
		require.Len(t, results, 3)

		// Verify we have 2 last_for_status results (one for status up, one for status down)
		lastResults := getLastForStatusResults()
		require.Len(t, lastResults, 2, "should have 2 results with last_for_status=true (one per status)")

		// Verify status up still has its last result
		var statusUpResults []*models.Result
		err = dbSvc.DB().NewSelect().
			Model(&statusUpResults).
			Where("check_uid = ?", check.UID).
			Where("status = ?", int(models.ResultStatusUp)).
			Where("last_for_status = ?", true).
			Scan(ctx)
		require.NoError(t, err)
		require.Len(t, statusUpResults, 1)

		// Verify status=2 has its last result
		var status2Results []*models.Result
		err = dbSvc.DB().NewSelect().
			Model(&status2Results).
			Where("check_uid = ?", check.UID).
			Where("status = ?", 2).
			Where("last_for_status = ?", true).
			Scan(ctx)
		require.NoError(t, err)
		require.Len(t, status2Results, 1)
		assert.Equal(t, result3.UID, status2Results[0].UID)
	})
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
	runner.worker = worker

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
		lastForStatus := true
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
			LastForStatus:   &lastForStatus,
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
		err = runner.executeHeartbeatJob(ctx, logger, checkJob)
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
		lastForStatus := true
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
			LastForStatus:   &lastForStatus,
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
		err = runner.executeHeartbeatJob(ctx, logger, checkJob)
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
	require.NotNil(t, runner.worker, "worker should be registered")

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
		Where("uid = ?", runner.worker.UID).
		Scan(ctx)
	require.NoError(t, err)
	require.NotNil(t, dbWorker.LastActiveAt, "worker should have last_active_at timestamp")
}
