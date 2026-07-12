package jobtypes

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

// newJobsCleanupTestContext wires an in-memory SQLite DB, a real job service, a
// services registry, and a logger so the cleanup runner exercises its full path
// (window resolution, both delete passes, self-reschedule).
func newJobsCleanupTestContext(t *testing.T) (*jobdef.JobContext, db.Service) {
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
		Logger:    slog.Default(),
	}

	return jctx, dbSvc
}

// seedJob inserts a job with fully controlled status/timestamps directly, so the
// test can place rows on either side of a retention window.
func seedJob(
	t *testing.T, dbSvc db.Service, status models.JobStatus, updatedAgo time.Duration, deletedAgo *time.Duration,
) *models.Job {
	t.Helper()

	job := models.NewJob(nil, "seed")
	job.Status = status
	job.UpdatedAt = time.Now().Add(-updatedAgo)
	job.CreatedAt = job.UpdatedAt
	if deletedAgo != nil {
		d := time.Now().Add(-*deletedAgo)
		job.DeletedAt = &d
	}

	_, err := dbSvc.DB().NewInsert().Model(job).Exec(t.Context())
	require.NoError(t, err)

	return job
}

func jobExists(t *testing.T, dbSvc db.Service, uid string) bool {
	t.Helper()

	n, err := dbSvc.DB().NewSelect().Model((*models.Job)(nil)).Where("uid = ?", uid).Count(t.Context())
	require.NoError(t, err)

	return n > 0
}

func jobDeletedAt(t *testing.T, dbSvc db.Service, uid string) *time.Time {
	t.Helper()

	var job models.Job
	require.NoError(t, dbSvc.DB().NewSelect().Model(&job).Where("uid = ?", uid).Scan(t.Context()))

	return job.DeletedAt
}

func countPendingCleanupJobs(ctx context.Context, dbSvc db.Service) (int, error) {
	return dbSvc.DB().NewSelect().
		Model((*models.Job)(nil)).
		Where("type = ?", string(jobdef.JobTypeJobsCleanup)).
		Where("status = ?", models.JobStatusPending).
		Where("deleted_at IS NULL").
		Count(ctx)
}

// TestJobsCleanupRunDefaultWindowsAndReschedule verifies the runner soft-deletes
// finished jobs past 48h, hard-deletes rows soft-deleted past 24h, and schedules
// exactly one follow-up run — all with the default (unconfigured) windows.
func TestJobsCleanupRunDefaultWindowsAndReschedule(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc := newJobsCleanupTestContext(t)

	h25 := 25 * time.Hour
	oldFinished := seedJob(t, dbSvc, models.JobStatusSuccess, 49*time.Hour, nil) // → soft-deleted
	recentFinished := seedJob(t, dbSvc, models.JobStatusSuccess, 10*time.Hour, nil)
	oldPending := seedJob(t, dbSvc, models.JobStatusPending, 49*time.Hour, nil)
	softedLongAgo := seedJob(t, dbSvc, models.JobStatusSuccess, 49*time.Hour, &h25) // → hard-deleted

	run := &JobsCleanupJobRun{}
	r.NoError(run.Run(t.Context(), jctx))

	r.NotNil(jobDeletedAt(t, dbSvc, oldFinished.UID), "finished > 48h soft-deleted")
	r.Nil(jobDeletedAt(t, dbSvc, recentFinished.UID), "recent finished untouched")
	r.Nil(jobDeletedAt(t, dbSvc, oldPending.UID), "pending never soft-deleted")
	r.False(jobExists(t, dbSvc, softedLongAgo.UID), "soft-deleted > 24h hard-deleted")

	pending, err := countPendingCleanupJobs(t.Context(), dbSvc)
	r.NoError(err)
	r.Equal(1, pending, "runner reschedules exactly one follow-up run")
}

// TestJobsCleanupSoftDeleteWindowFromDBParameter proves a global
// performance.jobs_soft_delete_hours parameter shrinks the window at run time,
// with no restart: a job finished 2h ago is spared by the 48h default but
// soft-deleted once the parameter is 1h.
func TestJobsCleanupSoftDeleteWindowFromDBParameter(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc := newJobsCleanupTestContext(t)

	job := seedJob(t, dbSvc, models.JobStatusSuccess, 2*time.Hour, nil)

	// Default window (48h) spares it.
	r.NoError((&JobsCleanupJobRun{}).Run(t.Context(), jctx))
	r.Nil(jobDeletedAt(t, dbSvc, job.UID), "default 48h window spares a 2h-old job")

	// A 1h global parameter now covers it.
	r.NoError(dbSvc.SetSystemParameter(t.Context(), string(systemconfig.KeyPerfJobsSoftDeleteHours), 1, false))
	r.NoError((&JobsCleanupJobRun{}).Run(t.Context(), jctx))
	r.NotNil(jobDeletedAt(t, dbSvc, job.UID), "1h parameter soft-deletes the 2h-old job without a restart")
}

// TestJobsCleanupSoftDeleteWindowEnvWinsOverDBParameter proves the env override
// beats the DB parameter: a large DB window would spare the job, but the small
// env window soft-deletes it.
func TestJobsCleanupSoftDeleteWindowEnvWinsOverDBParameter(t *testing.T) {
	r := require.New(t)
	jctx, dbSvc := newJobsCleanupTestContext(t)

	job := seedJob(t, dbSvc, models.JobStatusSuccess, 2*time.Hour, nil)

	r.NoError(dbSvc.SetSystemParameter(t.Context(), string(systemconfig.KeyPerfJobsSoftDeleteHours), 100, false))
	t.Setenv("SP_PERFORMANCE_JOBS_SOFT_DELETE_HOURS", "1")

	r.NoError((&JobsCleanupJobRun{}).Run(t.Context(), jctx))
	r.NotNil(jobDeletedAt(t, dbSvc, job.UID), "env (1h) wins over the DB parameter (100h)")
}

// TestJobsCleanupInvalidDBParameterFallsBackToDefault proves an invalid (< 1)
// parameter is ignored (logged as a warning) and the hardcoded 48h default
// applies rather than the nonsensical value: a 49h-old job is still soft-deleted
// while a 2h-old job survives.
func TestJobsCleanupInvalidDBParameterFallsBackToDefault(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc := newJobsCleanupTestContext(t)

	r.NoError(dbSvc.SetSystemParameter(t.Context(), string(systemconfig.KeyPerfJobsSoftDeleteHours), 0, false))

	oldJob := seedJob(t, dbSvc, models.JobStatusSuccess, 49*time.Hour, nil)
	youngJob := seedJob(t, dbSvc, models.JobStatusSuccess, 2*time.Hour, nil)

	r.NoError((&JobsCleanupJobRun{}).Run(t.Context(), jctx))

	r.NotNil(jobDeletedAt(t, dbSvc, oldJob.UID), "default 48h still soft-deletes a 49h-old job")
	r.Nil(jobDeletedAt(t, dbSvc, youngJob.UID), "invalid 0 falls back to default, not delete-everything")
}

// TestJobsCleanupBatchingDrainsBacklogInOneRun proves the run's per-stage loop
// keeps issuing batches until the backlog is drained: with a batch size of 2 and
// 5 eligible rows, one Run soft-deletes all 5.
func TestJobsCleanupBatchingDrainsBacklogInOneRun(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc := newJobsCleanupTestContext(t)

	seeds := make([]*models.Job, 0, 5)
	for range 5 {
		seeds = append(seeds, seedJob(t, dbSvc, models.JobStatusSuccess, 49*time.Hour, nil))
	}

	run := &JobsCleanupJobRun{batchSize: 2}
	r.NoError(run.Run(t.Context(), jctx))

	for _, s := range seeds {
		r.NotNil(jobDeletedAt(t, dbSvc, s.UID), "every backlog row soft-deleted in a single run")
	}
}

// TestEnsureJobsCleanupJobNoDuplicate proves the startup bootstrap doesn't stack
// duplicates — two calls leave exactly one pending jobs_cleanup job.
func TestEnsureJobsCleanupJobNoDuplicate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc := newJobsCleanupTestContext(t)

	startup := &StartupJobRun{}
	r.NoError(startup.ensureJobsCleanupJob(t.Context(), jctx))
	r.NoError(startup.ensureJobsCleanupJob(t.Context(), jctx))

	pending, err := countPendingCleanupJobs(t.Context(), dbSvc)
	r.NoError(err)
	r.Equal(1, pending, "ensureJobsCleanupJob must not stack duplicates")
}

// TestJobsCleanupRegistered proves the job type resolves through the registry.
func TestJobsCleanupRegistered(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	def, ok := GetJobDefinition(jobdef.JobTypeJobsCleanup)
	r.True(ok, "jobs_cleanup must be registered")
	r.Equal(jobdef.JobTypeJobsCleanup, def.Type())

	runner, err := def.CreateJobRun(nil)
	r.NoError(err)
	r.NotNil(runner)
}
