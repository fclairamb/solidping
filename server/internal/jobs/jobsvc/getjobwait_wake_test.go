package jobsvc_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// newTestJobService wires a fresh in-memory SQLite service + local notifier,
// matching the setup in getjobwait_leak_test.go.
func newTestJobService(t *testing.T) (jobsvc.Service, *sqlite.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	bus := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = bus.Close() })

	svc := jobsvc.NewService(dbSvc.DB(), dbSvc, bus, nil)

	return svc, dbSvc
}

// TestGetJobWaitClaimsFutureJobNearDueTime is spec 2026-09-25-07 Tests item 1:
// a job scheduled for the near future used to only be picked up by the
// 5-minute fallback ticker (or an unrelated job.created signal). With
// GetJobWait timing its wait off the earliest pending job, it must be claimed
// within ~0.5s of becoming due.
func TestGetJobWaitClaimsFutureJobNearDueTime(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc := newTestJobService(t)

	dueAt := time.Now().Add(2 * time.Second)
	job := models.NewJob(nil, "email")
	job.Status = models.JobStatusPending
	job.ScheduledAt = dueAt
	_, err := dbSvc.DB().NewInsert().Model(job).Exec(ctx)
	r.NoError(err)

	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	start := time.Now()
	claimed, err := svc.GetJobWait(waitCtx)
	r.NoError(err)
	r.NotNil(claimed)
	r.Equal(job.UID, claimed.UID)

	claimLatency := time.Since(dueAt)
	r.Lessf(claimLatency, 500*time.Millisecond,
		"job claimed %s after its scheduled_at, want < 500ms (took %s total)",
		claimLatency, time.Since(start))
}

// TestGetJobWaitRecomputesWaitOnEarlierJob is spec 2026-09-25-07 Tests item 3:
// while GetJobWait is sitting on a long wait for a far-future job, a new job
// with an EARLIER scheduled_at must still be claimed at its own due time — the
// job.created wake-up re-enters the loop, which recomputes the wait against
// the new earliest pending job.
func TestGetJobWaitRecomputesWaitOnEarlierJob(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc := newTestJobService(t)

	// Far-future job: without the recompute, GetJobWait would be sitting on a
	// multi-second (in production, up to 5-minute) timer for this one.
	farJob := models.NewJob(nil, "email")
	farJob.Status = models.JobStatusPending
	farJob.ScheduledAt = time.Now().Add(30 * time.Second)
	_, err := dbSvc.DB().NewInsert().Model(farJob).Exec(ctx)
	r.NoError(err)

	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	type result struct {
		job *models.Job
		err error
	}
	resultCh := make(chan result, 1)

	go func() {
		job, waitErr := svc.GetJobWait(waitCtx)
		resultCh <- result{job: job, err: waitErr}
	}()

	// Give GetJobWait time to claim-miss on farJob, subscribe to job.created
	// and settle into its (long, far-future) wait before the earlier job is
	// created — otherwise the wake-up notification fires before anyone is
	// listening for it.
	time.Sleep(100 * time.Millisecond)

	// A distinct, non-empty config keeps CreateJob's dedup lookup (which
	// matches on type+config+pending+org) from finding farJob and just
	// bumping its scheduled_at in place — that update path is a separate,
	// pre-existing gap that doesn't emit job.created at all (only the
	// brand-new-row insert path does), which is not what this test means to
	// exercise. A distinct config forces the real "new job" insert path.
	nearDueAt := time.Now().Add(1 * time.Second)
	_, err = svc.CreateJob(ctx, "", "email", []byte(`{"distinct":"near"}`), &jobsvc.JobOptions{ScheduledAt: &nearDueAt})
	r.NoError(err)

	select {
	case res := <-resultCh:
		r.NoError(res.err)
		r.NotNil(res.job)
		r.NotEqual(farJob.UID, res.job.UID, "must claim the newer, earlier-due job first")

		claimLatency := time.Since(nearDueAt)
		r.Lessf(claimLatency, 500*time.Millisecond,
			"earlier job claimed %s after its own scheduled_at, want < 500ms", claimLatency)
	case <-time.After(5 * time.Second):
		t.Fatal("GetJobWait did not return within 5s of the earlier job being created")
	}
}

// TestGetJobWaitSelfReschedulingSweepRunsAtItsOwnInterval is spec
// 2026-09-25-07 Tests item 2: a self-rescheduling sweep job (claim, run,
// immediately re-enqueue itself at now + interval — the shape every
// per-minute sweep in this codebase uses) must run at close to its own
// interval, not the old ~5-minute-fallback cadence. Interval is shortened to
// 40ms so the test proves "runs about N times per shortenedInterval" without
// a real 1-minute (let alone 5-minute) wait.
func TestGetJobWaitSelfReschedulingSweepRunsAtItsOwnInterval(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, _ := newTestJobService(t)

	const interval = 80 * time.Millisecond
	const runWindow = 1200 * time.Millisecond
	const wantRuns = int(runWindow / interval) // ~15

	// Seed the sweep's first run, due immediately.
	_, err := svc.CreateJob(ctx, "", "self_sweep", nil, nil)
	r.NoError(err)

	waitCtx, cancel := context.WithTimeout(ctx, runWindow+2*time.Second)
	defer cancel()

	runTimes := []time.Time{}
	nextConfig := json.RawMessage(`{}`)
	start := time.Now()

	for time.Since(start) < runWindow {
		claimed, waitErr := svc.GetJobWait(waitCtx)
		if waitErr != nil {
			break // context deadline: the run window is over.
		}

		runTimes = append(runTimes, time.Now())

		r.NoError(svc.CompleteRunningJob(ctx, claimed, models.JobStatusSuccess, nil))

		// Self-reschedule: exactly the pattern every per-minute sweep in this
		// codebase uses (insert the next run at now + interval).
		nextAt := time.Now().Add(interval)
		_, createErr := svc.CreateJob(ctx, "", "self_sweep", nextConfig,
			&jobsvc.JobOptions{ScheduledAt: &nextAt})
		r.NoError(createErr)
	}

	r.GreaterOrEqualf(len(runTimes), wantRuns/4,
		"self-rescheduling sweep at a %s interval ran only %d times in %s (wanted at least %d) — "+
			"this is the exact regression: a fixed 5-minute fallback would produce ~0-1 runs here",
		interval, len(runTimes), runWindow, wantRuns/3)
}
