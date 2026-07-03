package jobsvc_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// newQueueDepthTestService spins up an in-memory SQLite DB and returns the job
// service plus the db handle so tests can seed rows directly.
func newQueueDepthTestService(t *testing.T) (context.Context, *require.Assertions, *sqlite.Service, jobsvc.Service) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	svc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	return ctx, r, dbSvc, svc
}

// seedJob inserts a job row directly with the given status and deleted flag.
func seedJob(
	ctx context.Context, r *require.Assertions, dbSvc *sqlite.Service,
	status models.JobStatus, deleted bool,
) {
	job := models.NewJob(nil, "email")
	job.Status = status
	if deleted {
		now := time.Now()
		job.DeletedAt = &now
	}

	_, err := dbSvc.DB().NewInsert().Model(job).Exec(ctx)
	r.NoError(err)
}

func TestCountQueueDepth(t *testing.T) {
	t.Parallel()

	ctx, r, dbSvc, svc := newQueueDepthTestService(t)

	// Seed: 3 pending, 2 running, 1 success (excluded), 1 soft-deleted pending
	// (excluded), 1 soft-deleted running (excluded), 1 failed (excluded).
	seedJob(ctx, r, dbSvc, models.JobStatusPending, false)
	seedJob(ctx, r, dbSvc, models.JobStatusPending, false)
	seedJob(ctx, r, dbSvc, models.JobStatusPending, false)
	seedJob(ctx, r, dbSvc, models.JobStatusRunning, false)
	seedJob(ctx, r, dbSvc, models.JobStatusRunning, false)
	seedJob(ctx, r, dbSvc, models.JobStatusSuccess, false)
	seedJob(ctx, r, dbSvc, models.JobStatusFailed, false)
	seedJob(ctx, r, dbSvc, models.JobStatusPending, true)
	seedJob(ctx, r, dbSvc, models.JobStatusRunning, true)

	depth, err := svc.CountQueueDepth(ctx)
	r.NoError(err)

	r.Equal(3, depth[models.JobStatusPending])
	r.Equal(2, depth[models.JobStatusRunning])
	// Terminal/soft-deleted statuses are never surfaced.
	_, hasSuccess := depth[models.JobStatusSuccess]
	r.False(hasSuccess)
	_, hasFailed := depth[models.JobStatusFailed]
	r.False(hasFailed)
}

func TestCountQueueDepthZeroFill(t *testing.T) {
	t.Parallel()

	ctx, r, dbSvc, svc := newQueueDepthTestService(t)

	// Only running jobs exist — pending must still report 0, not be absent.
	seedJob(ctx, r, dbSvc, models.JobStatusRunning, false)

	depth, err := svc.CountQueueDepth(ctx)
	r.NoError(err)

	pending, hasPending := depth[models.JobStatusPending]
	r.True(hasPending, "pending must be present even with no rows")
	r.Equal(0, pending)
	r.Equal(1, depth[models.JobStatusRunning])
}

func TestCountQueueDepthEmpty(t *testing.T) {
	t.Parallel()

	ctx, r, _, svc := newQueueDepthTestService(t)

	depth, err := svc.CountQueueDepth(ctx)
	r.NoError(err)

	r.Equal(0, depth[models.JobStatusPending])
	r.Equal(0, depth[models.JobStatusRunning])
}
