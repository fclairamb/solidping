package jobsvc_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// reapTestEnv bundles a db.Service, its job service and a require helper for one
// backend so the same scenarios run on both SQLite and Postgres.
type reapTestEnv struct {
	ctx   context.Context
	r     *require.Assertions
	dbSvc db.Service
	svc   jobsvc.Service
}

// seedRunningJob inserts a job directly in 'running' with the given retry_count
// and updated_at, bypassing the service so we control the exact row state.
func (e *reapTestEnv) seedRunningJob(retryCount int, updatedAt time.Time) *models.Job {
	job := models.NewJob(nil, "email")
	job.Status = models.JobStatusRunning
	job.RetryCount = retryCount
	job.UpdatedAt = updatedAt
	job.CreatedAt = updatedAt

	_, err := e.dbSvc.DB().NewInsert().Model(job).Exec(e.ctx)
	e.r.NoError(err)

	return job
}

// getJobRow re-reads a job row directly (no deleted_at filter) so we can assert
// on terminal status and output.
func (e *reapTestEnv) getJobRow(uid string) *models.Job {
	var job models.Job
	err := e.dbSvc.DB().NewSelect().Model(&job).Where("uid = ?", uid).Scan(e.ctx)
	e.r.NoError(err)

	return &job
}

// countClones returns how many jobs point at parentUID via previous_job_uid.
func (e *reapTestEnv) countClones(parentUID string) int {
	count, err := e.dbSvc.DB().NewSelect().
		Model((*models.Job)(nil)).
		Where("previous_job_uid = ?", parentUID).
		Count(e.ctx)
	e.r.NoError(err)

	return count
}

// runReapSuite runs every reaper scenario against one backend. parallel governs
// whether subtests run in parallel — only safe when makeSvc hands each subtest
// an isolated database (SQLite in-memory). A shared Postgres instance must run
// serially so the table-wide sweep does not observe sibling subtests' rows.
func runReapSuite(t *testing.T, parallel bool, makeSvc func(t *testing.T) db.Service) {
	t.Helper()

	timeout := 15 * time.Minute
	stale := func() time.Time { return time.Now().Add(-30 * time.Minute) }
	fresh := func() time.Time { return time.Now().Add(-time.Minute) }

	newEnv := func(t *testing.T) *reapTestEnv {
		t.Helper()
		if parallel {
			t.Parallel()
		}
		dbSvc := makeSvc(t)
		svc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier())

		return &reapTestEnv{ctx: t.Context(), r: require.New(t), dbSvc: dbSvc, svc: svc}
	}

	t.Run("RetriesRemaining_RetriedWithClone", func(t *testing.T) {
		e := newEnv(t)

		job := e.seedRunningJob(0, stale())

		res, err := e.svc.ReapStuckJobs(e.ctx, timeout)
		e.r.NoError(err)
		e.r.Equal(1, res.Retried)
		e.r.Equal(0, res.Failed)

		original := e.getJobRow(job.UID)
		e.r.Equal(models.JobStatusRetried, original.Status)
		e.r.Equal(1, e.countClones(job.UID), "exactly one backoff clone should exist")
	})

	t.Run("CapReached_FailedWithReason", func(t *testing.T) {
		e := newEnv(t)

		job := e.seedRunningJob(jobsvc.MaxRetryCount, stale())

		res, err := e.svc.ReapStuckJobs(e.ctx, timeout)
		e.r.NoError(err)
		e.r.Equal(0, res.Retried)
		e.r.Equal(1, res.Failed)

		original := e.getJobRow(job.UID)
		e.r.Equal(models.JobStatusFailed, original.Status)
		e.r.Equal("stuck_timeout", original.Output["reason"])
		e.r.Equal(0, e.countClones(job.UID), "a failed (capped) job must not be cloned")
	})

	t.Run("WithinTimeout_Untouched", func(t *testing.T) {
		e := newEnv(t)

		job := e.seedRunningJob(0, fresh())

		res, err := e.svc.ReapStuckJobs(e.ctx, timeout)
		e.r.NoError(err)
		e.r.Equal(0, res.Retried)
		e.r.Equal(0, res.Failed)

		original := e.getJobRow(job.UID)
		e.r.Equal(models.JobStatusRunning, original.Status, "a job within the timeout must not be reaped")
	})

	t.Run("NonRunningStatuses_NeverReaped", func(t *testing.T) {
		e := newEnv(t)

		for _, status := range []models.JobStatus{
			models.JobStatusPending, models.JobStatusSuccess, models.JobStatusFailed,
		} {
			job := models.NewJob(nil, "email")
			job.Status = status
			job.UpdatedAt = stale()
			job.CreatedAt = stale()
			_, err := e.dbSvc.DB().NewInsert().Model(job).Exec(e.ctx)
			e.r.NoError(err)
		}

		res, err := e.svc.ReapStuckJobs(e.ctx, timeout)
		e.r.NoError(err)
		e.r.Equal(0, res.Retried)
		e.r.Equal(0, res.Failed)
	})

	t.Run("AntiClobber_WorkerLosesToReaper", func(t *testing.T) {
		e := newEnv(t)

		// A stuck job at the retry cap. The reaper fails it.
		job := e.seedRunningJob(jobsvc.MaxRetryCount, stale())

		res, err := e.svc.ReapStuckJobs(e.ctx, timeout)
		e.r.NoError(err)
		e.r.Equal(1, res.Failed)

		// The worker, finishing late, tries to write a terminal success. Because
		// the row is no longer 'running', the guarded write must be rejected.
		completeErr := e.svc.CompleteRunningJob(e.ctx, job, models.JobStatusSuccess,
			json.RawMessage(`{"ok":true}`))
		e.r.ErrorIs(completeErr, jobsvc.ErrJobLeaseLost)

		// Status stays whatever the reaper decided.
		after := e.getJobRow(job.UID)
		e.r.Equal(models.JobStatusFailed, after.Status, "worker must not clobber the reaper's decision")
	})

	t.Run("Idempotent_SecondSweepNoOp", func(t *testing.T) {
		e := newEnv(t)

		job := e.seedRunningJob(0, stale())

		first, err := e.svc.ReapStuckJobs(e.ctx, timeout)
		e.r.NoError(err)
		e.r.Equal(1, first.Retried)

		// A second sweep finds nothing still running -> no double-action.
		second, err := e.svc.ReapStuckJobs(e.ctx, timeout)
		e.r.NoError(err)
		e.r.Equal(0, second.Retried)
		e.r.Equal(0, second.Failed)
		e.r.Equal(1, e.countClones(job.UID), "no extra clone on a repeated sweep")
	})

	t.Run("CompleteRunningJob_HappyPath", func(t *testing.T) {
		e := newEnv(t)

		job := e.seedRunningJob(0, fresh())

		err := e.svc.CompleteRunningJob(e.ctx, job, models.JobStatusSuccess,
			json.RawMessage(`{"ok":true}`))
		e.r.NoError(err)

		after := e.getJobRow(job.UID)
		e.r.Equal(models.JobStatusSuccess, after.Status)
	})
}

func TestReapStuckJobsSQLite(t *testing.T) {
	t.Parallel()

	// SQLite: each subtest gets a fresh isolated in-memory DB, so subtests run
	// in parallel safely.
	runReapSuite(t, true, func(t *testing.T) db.Service {
		t.Helper()
		ctx := t.Context()
		dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
		require.NoError(t, err)
		require.NoError(t, dbSvc.Initialize(ctx))
		t.Cleanup(func() { _ = dbSvc.Close() })

		return dbSvc
	})
}

func TestReapStuckJobsPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL reaper test in short mode")
	}

	t.Parallel()

	tempDir, err := os.MkdirTemp("", "jobsvc-reap-pg-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })

	ctx := t.Context()
	dbSvc, err := postgres.NewEmbedded(ctx, tempDir, 5447, false, "", false)
	require.NoError(t, err, "failed to start embedded postgres")
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	// Postgres is a single shared instance here. Run the suite serially
	// (parallel=false) and truncate the jobs table before each subtest so the
	// table-wide sweep never observes a sibling subtest's rows.
	runReapSuite(t, false, func(t *testing.T) db.Service {
		t.Helper()

		_, err := dbSvc.DB().NewDelete().Model((*models.Job)(nil)).Where("1 = 1").Exec(ctx)
		require.NoError(t, err)

		return dbSvc
	})
}
