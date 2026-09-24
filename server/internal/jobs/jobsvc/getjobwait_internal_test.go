package jobsvc

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// newInternalTestService wires a fresh in-memory SQLite serviceImpl directly
// (bypassing NewService) so these white-box tests can reach unexported
// members: nextPendingWait and the claimAttempted test hook.
func newInternalTestService(t *testing.T) (*serviceImpl, *sqlite.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	bus := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = bus.Close() })

	return &serviceImpl{db: dbSvc.DB(), dbSvc: dbSvc, notifier: bus}, dbSvc
}

// TestNextPendingWaitFloorsAndCaps is spec 2026-09-25-07 Tests item 2 (plus
// the "capped by the existing 5-minute fallback" half of item 1): with no
// pending job the wait falls back to getJobWaitFallback; a job due inside the
// floor window is floored to getJobWaitMinWait instead of yielding a
// near-zero or negative wait; a job far in the future is capped at
// getJobWaitFallback rather than sleeping past it.
func TestNextPendingWaitFloorsAndCaps(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc := newInternalTestService(t)

	r.Equal(getJobWaitFallback, svc.nextPendingWait(ctx),
		"no pending job must fall back to the 5-minute cap")

	soonJob := models.NewJob(nil, "email")
	soonJob.Status = models.JobStatusPending
	soonJob.ScheduledAt = time.Now().Add(10 * time.Millisecond)
	_, err := dbSvc.DB().NewInsert().Model(soonJob).Exec(ctx)
	r.NoError(err)

	wait := svc.nextPendingWait(ctx)
	r.GreaterOrEqualf(wait, getJobWaitMinWait, "10ms-out job must be floored, got %s", wait)
	r.LessOrEqual(wait, getJobWaitFallback)

	_, err = dbSvc.DB().NewDelete().Model((*models.Job)(nil)).Where("uid = ?", soonJob.UID).Exec(ctx)
	r.NoError(err)

	farJob := models.NewJob(nil, "email")
	farJob.Status = models.JobStatusPending
	farJob.ScheduledAt = time.Now().Add(24 * time.Hour)
	_, err = dbSvc.DB().NewInsert().Model(farJob).Exec(ctx)
	r.NoError(err)

	r.Equal(getJobWaitFallback, svc.nextPendingWait(ctx),
		"a job a day out must be capped at the 5-minute fallback, not slept past it")
}

// TestGetJobWaitDoesNotSpinWhenDueJobLosesTheRace is spec 2026-09-25-07 Tests
// item 4: a due job that another runner claims first must not make GetJobWait
// spin. Real SKIP LOCKED contention needs Postgres and two concurrent
// transactions; here the claimAttempted test hook deterministically simulates
// the same outcome — "the row is gone by the time we look" — on SQLite: the
// job is scheduled inside the floor window, so GetJobWait's first claim
// attempt (t=0) correctly misses (not due yet) and floors its next wait to
// getJobWaitMinWait; exactly as the second attempt starts (now genuinely due),
// the hook flips the row to 'running' out of band, standing in for a
// concurrent runner that won the race a moment earlier. From there on the job
// is invisible to both claimNextJob and nextPendingWait, which falls back to
// getJobWaitFallback — so without a floor bounding the first retry, and with
// the fallback correctly excluding an already-claimed row, the total attempt
// count over the test's short window is exactly 2, never a tight spin.
func TestGetJobWaitDoesNotSpinWhenDueJobLosesTheRace(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc := newInternalTestService(t)

	job := models.NewJob(nil, "email")
	job.Status = models.JobStatusPending
	job.ScheduledAt = time.Now().Add(20 * time.Millisecond)
	_, err := dbSvc.DB().NewInsert().Model(job).Exec(ctx)
	r.NoError(err)

	var attempts atomic.Int64

	svc.claimAttempted = func() {
		if n := attempts.Add(1); n == 2 {
			_, updErr := dbSvc.DB().NewUpdate().
				Model((*models.Job)(nil)).
				Set("status = ?", models.JobStatusRunning).
				Where("uid = ?", job.UID).
				Exec(context.Background())
			r.NoError(updErr)
		}
	}

	waitCtx, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer cancel()

	_, err = svc.GetJobWait(waitCtx)
	r.ErrorIs(err, context.DeadlineExceeded)

	r.Equal(int64(2), attempts.Load(),
		"expected exactly 2 claim attempts (initial miss + the floored retry that loses the race); "+
			"a higher count means the loop is spinning")
}
