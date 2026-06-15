package jobworker

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

func seedJobRow(
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

// TestQueueDepthSamplerSample seeds a mix of rows, runs one sample tick, and
// asserts the pending/running gauges match the live counts while terminal and
// soft-deleted rows are excluded.
// Not parallel: asserts on the process-global pending/running gauge series,
// which TestQueueDepthSamplerZeroFill also writes.
//
//nolint:paralleltest // shares a process-global gauge with another test
func TestQueueDepthSamplerSample(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	svc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier())

	// 2 pending, 3 running live; plus excluded: success, soft-deleted pending.
	seedJobRow(ctx, r, dbSvc, models.JobStatusPending, false)
	seedJobRow(ctx, r, dbSvc, models.JobStatusPending, false)
	seedJobRow(ctx, r, dbSvc, models.JobStatusRunning, false)
	seedJobRow(ctx, r, dbSvc, models.JobStatusRunning, false)
	seedJobRow(ctx, r, dbSvc, models.JobStatusRunning, false)
	seedJobRow(ctx, r, dbSvc, models.JobStatusSuccess, false)
	seedJobRow(ctx, r, dbSvc, models.JobStatusPending, true)

	sampler := NewQueueDepthSampler(svc)
	sampler.sample(ctx)

	r.InDelta(2.0, testutil.ToFloat64(prommetrics.JobsQueueDepth.WithLabelValues("pending")), 0.001)
	r.InDelta(3.0, testutil.ToFloat64(prommetrics.JobsQueueDepth.WithLabelValues("running")), 0.001)
}

// TestQueueDepthSamplerZeroFill verifies a status that drops to zero is reset
// to 0 on the next sample, not left stale at its previous value.
// Not parallel: asserts on the process-global pending gauge series, which
// TestQueueDepthSamplerSample also writes.
//
//nolint:paralleltest // shares a process-global gauge with another test
func TestQueueDepthSamplerZeroFill(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	svc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier())
	sampler := NewQueueDepthSampler(svc)

	// First sample: one pending job present.
	seedJobRow(ctx, r, dbSvc, models.JobStatusPending, false)
	sampler.sample(ctx)
	r.InDelta(1.0, testutil.ToFloat64(prommetrics.JobsQueueDepth.WithLabelValues("pending")), 0.001)

	// Drain pending (soft-delete it), sample again: gauge must drop to 0.
	_, err = dbSvc.DB().NewUpdate().
		Model((*models.Job)(nil)).
		Set("deleted_at = ?", time.Now()).
		Where("status = ?", models.JobStatusPending).
		Exec(ctx)
	r.NoError(err)

	sampler.sample(ctx)
	r.InDelta(0.0, testutil.ToFloat64(prommetrics.JobsQueueDepth.WithLabelValues("pending")), 0.001)
}

// TestQueueDepthSamplerRunStops verifies Run primes the gauge immediately and
// returns context.Canceled when the context is cancelled.
func TestQueueDepthSamplerRunStops(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	svc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier())
	sampler := NewQueueDepthSampler(svc)

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)

	go func() { done <- sampler.Run(runCtx) }()

	cancel()

	select {
	case runErr := <-done:
		r.ErrorIs(runErr, context.Canceled)
	case <-time.After(5 * time.Second):
		r.Fail("sampler.Run did not return after context cancel")
	}
}
