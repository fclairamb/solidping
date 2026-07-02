package jobtypes_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// newReaperTestContext wires an in-memory SQLite DB, a real job service, a
// services registry, and an AppConfig so the reaper runner exercises its full
// path (config read, ReapStuckJobs, metrics, self-reschedule).
func newReaperTestContext(t *testing.T, stuckTimeout time.Duration) (*jobdef.JobContext, db.Service) {
	t.Helper()

	ctx := t.Context()
	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	jobSvc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	jctx := &jobdef.JobContext{
		Services:  &services.Registry{Jobs: jobSvc},
		DB:        dbSvc.DB(),
		DBService: dbSvc,
		AppConfig: &config.Config{
			Jobs: config.JobsConfig{StuckTimeout: stuckTimeout, ReaperInterval: time.Minute},
		},
		Logger: slog.Default(),
	}

	return jctx, dbSvc
}

func seedRunning(t *testing.T, dbSvc db.Service, retryCount int, age time.Duration) *models.Job {
	t.Helper()

	job := models.NewJob(nil, "email")
	job.Status = models.JobStatusRunning
	job.RetryCount = retryCount
	job.UpdatedAt = time.Now().Add(-age)
	job.CreatedAt = job.UpdatedAt

	_, err := dbSvc.DB().NewInsert().Model(job).Exec(t.Context())
	require.NoError(t, err)

	return job
}

// TestStuckJobReaperRunReapsAndReschedules verifies the runner reaps a stuck
// job (retried path), bumps the reaped metric, and schedules its next run.
func TestStuckJobReaperRunReapsAndReschedules(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	jctx, dbSvc := newReaperTestContext(t, 15*time.Minute)
	stuck := seedRunning(t, dbSvc, 0, 30*time.Minute)

	before := testutil.ToFloat64(prommetrics.JobsReaped.WithLabelValues("retried"))

	run := &jobtypes.StuckJobReaperJobRun{}
	r.NoError(run.Run(t.Context(), jctx))

	// Original job was reaped to 'retried'.
	var original models.Job
	r.NoError(dbSvc.DB().NewSelect().Model(&original).Where("uid = ?", stuck.UID).Scan(t.Context()))
	r.Equal(models.JobStatusRetried, original.Status)

	// Reaped metric advanced.
	after := testutil.ToFloat64(prommetrics.JobsReaped.WithLabelValues("retried"))
	r.InDelta(before+1, after, 0.0001)

	// The runner rescheduled itself: a pending stuck_job_reaper job now exists.
	pending, err := countPendingReaperJobs(t.Context(), dbSvc)
	r.NoError(err)
	r.Equal(1, pending, "reaper should reschedule exactly one follow-up run")
}

// TestStuckJobReaperRunNoServicesIsNoop verifies the runner degrades to a no-op
// (no panic, no error) when no job service is wired.
func TestStuckJobReaperRunNoServicesIsNoop(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	jctx := &jobdef.JobContext{Logger: slog.Default()}
	run := &jobtypes.StuckJobReaperJobRun{}
	r.NoError(run.Run(t.Context(), jctx))
}

// TestStuckJobReaperRunWithinTimeoutNoop verifies a fresh running job is not
// reaped and the reaped counter does not move.
func TestStuckJobReaperRunWithinTimeoutNoop(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	jctx, dbSvc := newReaperTestContext(t, 15*time.Minute)
	fresh := seedRunning(t, dbSvc, 0, time.Minute)

	run := &jobtypes.StuckJobReaperJobRun{}
	r.NoError(run.Run(t.Context(), jctx))

	var job models.Job
	r.NoError(dbSvc.DB().NewSelect().Model(&job).Where("uid = ?", fresh.UID).Scan(t.Context()))
	r.Equal(models.JobStatusRunning, job.Status, "a fresh running job must survive the sweep")
}

func countPendingReaperJobs(ctx context.Context, dbSvc db.Service) (int, error) {
	return dbSvc.DB().NewSelect().
		Model((*models.Job)(nil)).
		Where("type = ?", string(jobdef.JobTypeStuckJobReaper)).
		Where("status = ?", models.JobStatusPending).
		Where("deleted_at IS NULL").
		Count(ctx)
}
