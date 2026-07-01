package checkworker

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
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

// TestDelaySampleMs verifies the telemetry semantics of the delay sample
// (spec 2026-07-01-02 D4): lateness is measured against the job's real
// scheduled_at — the schedule the user configured — not the cost-padded
// effective_scheduled_at, and is floored at 0 (a probe that starts on time or
// is claimed ahead yields 0).
func TestDelaySampleMs(t *testing.T) {
	t.Parallel()

	w := &CheckWorker{} // delaySampleMs reads no receiver state
	now := time.Now()

	t.Run("OnTimeStartYieldsZero", func(t *testing.T) {
		t.Parallel()

		scheduledAt := now
		job := &models.CheckJob{ScheduledAt: &scheduledAt, EffectiveScheduledAt: &scheduledAt}
		require.Zero(t, w.delaySampleMs(job, now), "a probe starting exactly on schedule has 0 delay")
	})

	t.Run("EarlyStartFlooredAtZero", func(t *testing.T) {
		t.Parallel()

		scheduledAt := now.Add(10 * time.Second)
		job := &models.CheckJob{ScheduledAt: &scheduledAt}
		require.Zero(t, w.delaySampleMs(job, now), "a probe starting before schedule is floored at 0")
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
		require.InDelta(t, 5000.0, w.delaySampleMs(job, now), 1.0,
			"delay must be measured against scheduled_at, not effective_scheduled_at")
	})

	t.Run("NilScheduledAtFallsBackToEffective", func(t *testing.T) {
		t.Parallel()

		effective := now.Add(-3 * time.Second)
		job := &models.CheckJob{ScheduledAt: nil, EffectiveScheduledAt: &effective}
		require.InDelta(t, 3000.0, w.delaySampleMs(job, now), 1.0,
			"without a scheduled_at, the effective deadline is the fallback reference")
	})

	t.Run("NoReferenceYieldsZero", func(t *testing.T) {
		t.Parallel()

		job := &models.CheckJob{}
		require.Zero(t, w.delaySampleMs(job, now))
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
	runner.setWorker(worker)

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

	// Helper to get all results for this check (excluding initial "created" result)
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

	// Helper to get results with last_for_status = true (excluding initial "created" result)
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

		// Verify status down has its last result
		var status2Results []*models.Result
		err = dbSvc.DB().NewSelect().
			Model(&status2Results).
			Where("check_uid = ?", check.UID).
			Where("status = ?", int(models.ResultStatusDown)).
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
//nolint:paralleltest,cyclop // Uses shared database state and a live worker; sequential phases
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
	createSleepJob := func(slug string, sleepMs int, lane int16, costEWMAMs float64) (checkUID string) {
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
