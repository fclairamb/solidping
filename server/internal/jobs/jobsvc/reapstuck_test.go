package jobsvc_test

import (
	"context"
	"encoding/json"
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
// backend so the same scenarios run on both SQLite and Postgres. The context is
// derived from the test (t.Context()) rather than stored, to satisfy
// containedctx.
type reapTestEnv struct {
	t     *testing.T
	r     *require.Assertions
	dbSvc db.Service
	svc   jobsvc.Service
}

func (e *reapTestEnv) ctx() context.Context { return e.t.Context() }

// seedRunningJob inserts a job directly in 'running' with the given retry_count
// and updated_at, bypassing the service so we control the exact row state.
func (e *reapTestEnv) seedRunningJob(retryCount int, updatedAt time.Time) *models.Job {
	job := models.NewJob(nil, "email")
	job.Status = models.JobStatusRunning
	job.RetryCount = retryCount
	job.UpdatedAt = updatedAt
	job.CreatedAt = updatedAt

	_, err := e.dbSvc.DB().NewInsert().Model(job).Exec(e.ctx())
	e.r.NoError(err)

	return job
}

// getJobRow re-reads a job row directly (no deleted_at filter) so we can assert
// on terminal status and output.
func (e *reapTestEnv) getJobRow(uid string) *models.Job {
	var job models.Job
	err := e.dbSvc.DB().NewSelect().Model(&job).Where("uid = ?", uid).Scan(e.ctx())
	e.r.NoError(err)

	return &job
}

// countClones returns how many jobs point at parentUID via previous_job_uid.
func (e *reapTestEnv) countClones(parentUID string) int {
	count, err := e.dbSvc.DB().NewSelect().
		Model((*models.Job)(nil)).
		Where("previous_job_uid = ?", parentUID).
		Count(e.ctx())
	e.r.NoError(err)

	return count
}

// reapTimeout is the stuck-job timeout used by every scenario.
const reapTimeout = 15 * time.Minute

func staleTime() time.Time { return time.Now().Add(-30 * time.Minute) }
func freshTime() time.Time { return time.Now().Add(-time.Minute) }

// runReapSuite runs every reaper scenario against one backend, each subtest in
// its own isolated database (provided by makeSvc) so subtests can run in
// parallel safely.
func runReapSuite(t *testing.T, makeSvc func(t *testing.T) db.Service) {
	t.Helper()

	timeout := reapTimeout
	stale := staleTime
	fresh := freshTime

	newEnv := func(t *testing.T) *reapTestEnv {
		t.Helper()
		dbSvc := makeSvc(t)
		svc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

		return &reapTestEnv{t: t, r: require.New(t), dbSvc: dbSvc, svc: svc}
	}

	t.Run("RetriesRemaining_RetriedWithClone", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)

		job := e.seedRunningJob(0, stale())

		res, err := e.svc.ReapStuckJobs(e.ctx(), timeout)
		e.r.NoError(err)
		e.r.Equal(1, res.Retried)
		e.r.Equal(0, res.Failed)

		original := e.getJobRow(job.UID)
		e.r.Equal(models.JobStatusRetried, original.Status)
		e.r.Equal(1, e.countClones(job.UID), "exactly one backoff clone should exist")
	})

	t.Run("CapReached_FailedWithReason", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)

		job := e.seedRunningJob(jobsvc.MaxRetryCount, stale())

		res, err := e.svc.ReapStuckJobs(e.ctx(), timeout)
		e.r.NoError(err)
		e.r.Equal(0, res.Retried)
		e.r.Equal(1, res.Failed)

		original := e.getJobRow(job.UID)
		e.r.Equal(models.JobStatusFailed, original.Status)
		e.r.Equal("stuck_timeout", original.Output["reason"])
		e.r.Equal(0, e.countClones(job.UID), "a failed (capped) job must not be cloned")
	})

	t.Run("WithinTimeout_Untouched", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)

		job := e.seedRunningJob(0, fresh())

		res, err := e.svc.ReapStuckJobs(e.ctx(), timeout)
		e.r.NoError(err)
		e.r.Equal(0, res.Retried)
		e.r.Equal(0, res.Failed)

		original := e.getJobRow(job.UID)
		e.r.Equal(models.JobStatusRunning, original.Status, "a job within the timeout must not be reaped")
	})

	t.Run("NonRunningStatuses_NeverReaped", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)

		for _, status := range []models.JobStatus{
			models.JobStatusPending, models.JobStatusSuccess, models.JobStatusFailed,
		} {
			job := models.NewJob(nil, "email")
			job.Status = status
			job.UpdatedAt = stale()
			job.CreatedAt = stale()
			_, err := e.dbSvc.DB().NewInsert().Model(job).Exec(e.ctx())
			e.r.NoError(err)
		}

		res, err := e.svc.ReapStuckJobs(e.ctx(), timeout)
		e.r.NoError(err)
		e.r.Equal(0, res.Retried)
		e.r.Equal(0, res.Failed)
	})

	t.Run("AntiClobber_WorkerLosesToReaper", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)

		// A stuck job at the retry cap. The reaper fails it.
		job := e.seedRunningJob(jobsvc.MaxRetryCount, stale())

		res, err := e.svc.ReapStuckJobs(e.ctx(), timeout)
		e.r.NoError(err)
		e.r.Equal(1, res.Failed)

		// The worker, finishing late, tries to write a terminal success. Because
		// the row is no longer 'running', the guarded write must be rejected.
		completeErr := e.svc.CompleteRunningJob(e.ctx(), job, models.JobStatusSuccess,
			json.RawMessage(`{"ok":true}`))
		e.r.ErrorIs(completeErr, jobsvc.ErrJobLeaseLost)

		// Status stays whatever the reaper decided.
		after := e.getJobRow(job.UID)
		e.r.Equal(models.JobStatusFailed, after.Status, "worker must not clobber the reaper's decision")
	})

	t.Run("Idempotent_SecondSweepNoOp", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)

		job := e.seedRunningJob(0, stale())

		first, err := e.svc.ReapStuckJobs(e.ctx(), timeout)
		e.r.NoError(err)
		e.r.Equal(1, first.Retried)

		// A second sweep finds nothing still running -> no double-action.
		second, err := e.svc.ReapStuckJobs(e.ctx(), timeout)
		e.r.NoError(err)
		e.r.Equal(0, second.Retried)
		e.r.Equal(0, second.Failed)
		e.r.Equal(1, e.countClones(job.UID), "no extra clone on a repeated sweep")
	})

	t.Run("CompleteRunningJob_HappyPath", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t)

		job := e.seedRunningJob(0, fresh())

		err := e.svc.CompleteRunningJob(e.ctx(), job, models.JobStatusSuccess,
			json.RawMessage(`{"ok":true}`))
		e.r.NoError(err)

		after := e.getJobRow(job.UID)
		e.r.Equal(models.JobStatusSuccess, after.Status)
	})
}

func TestReapStuckJobsSQLite(t *testing.T) {
	t.Parallel()

	// SQLite: each subtest gets a fresh, isolated in-memory DB, so the suite's
	// subtests run in parallel safely.
	runReapSuite(t, func(t *testing.T) db.Service {
		t.Helper()
		ctx := t.Context()
		dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
		require.NoError(t, err)
		require.NoError(t, dbSvc.Initialize(ctx))
		t.Cleanup(func() { _ = dbSvc.Close() })

		return dbSvc
	})
}

// TestReapStuckJobsPostgres exercises the optimistic-guard SQL on Postgres. It
// runs the core scenarios sequentially in one body (not the parallel suite),
// because the table-wide sweep is shared state on a single embedded instance.
func TestReapStuckJobsPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PostgreSQL reaper test in short mode")
	}

	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := postgres.NewEmbedded(ctx, "jobsvc-reapstuck", 5447, false, "", false, 0)
	r.NoError(err, "failed to start embedded postgres")
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	svc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	e := &reapTestEnv{t: t, r: r, dbSvc: dbSvc, svc: svc}

	truncate := func() {
		_, delErr := dbSvc.DB().NewDelete().Model((*models.Job)(nil)).Where("1 = 1").Exec(ctx)
		r.NoError(delErr)
	}

	// Retries remaining -> retried + backoff clone.
	truncate()
	retriedJob := e.seedRunningJob(0, staleTime())
	res, err := svc.ReapStuckJobs(ctx, reapTimeout)
	r.NoError(err)
	r.Equal(1, res.Retried)
	r.Equal(models.JobStatusRetried, e.getJobRow(retriedJob.UID).Status)
	r.Equal(1, e.countClones(retriedJob.UID))

	// Cap reached -> failed with reason, no clone.
	truncate()
	failedJob := e.seedRunningJob(jobsvc.MaxRetryCount, staleTime())
	res, err = svc.ReapStuckJobs(ctx, reapTimeout)
	r.NoError(err)
	r.Equal(1, res.Failed)
	failedRow := e.getJobRow(failedJob.UID)
	r.Equal(models.JobStatusFailed, failedRow.Status)
	r.Equal("stuck_timeout", failedRow.Output["reason"])
	r.Equal(0, e.countClones(failedJob.UID))

	// Within timeout -> untouched.
	truncate()
	freshJob := e.seedRunningJob(0, freshTime())
	res, err = svc.ReapStuckJobs(ctx, reapTimeout)
	r.NoError(err)
	r.Equal(0, res.Retried)
	r.Equal(0, res.Failed)
	r.Equal(models.JobStatusRunning, e.getJobRow(freshJob.UID).Status)

	// Anti-clobber: the optimistic guard rejects the late worker write on PG.
	// Self-contained — seed, reap to failed, then attempt the stale completion.
	truncate()
	clobberJob := e.seedRunningJob(jobsvc.MaxRetryCount, staleTime())
	res, err = svc.ReapStuckJobs(ctx, reapTimeout)
	r.NoError(err)
	r.Equal(1, res.Failed)

	completeErr := svc.CompleteRunningJob(ctx, clobberJob, models.JobStatusSuccess,
		json.RawMessage(`{"ok":true}`))
	r.ErrorIs(completeErr, jobsvc.ErrJobLeaseLost)
	r.Equal(models.JobStatusFailed, e.getJobRow(clobberJob.UID).Status)
}
