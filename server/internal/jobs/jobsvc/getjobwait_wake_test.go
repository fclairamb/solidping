package jobsvc_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
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

// countingNotifier wraps an EventNotifier and counts how many times
// eventTypeJobCreated ("job.created") is emitted, so a test can assert on
// CreateJob's notify decision without racing a live GetJobWait goroutine.
type countingNotifier struct {
	notifier.EventNotifier

	jobCreatedCount atomic.Int64
}

func (c *countingNotifier) Notify(ctx context.Context, eventType, payload string) error {
	if eventType == "job.created" {
		c.jobCreatedCount.Add(1)
	}

	return c.EventNotifier.Notify(ctx, eventType, payload)
}

// newTestJobServiceWithCountingNotifier is newTestJobService, but exposes the
// countingNotifier wrapping the bus so a test can read how many job.created
// notifications a CreateJob call emitted.
func newTestJobServiceWithCountingNotifier(t *testing.T) (jobsvc.Service, *countingNotifier) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	bus := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = bus.Close() })

	counting := &countingNotifier{EventNotifier: bus}

	svc := jobsvc.NewService(dbSvc.DB(), dbSvc, counting, nil)

	return svc, counting
}

// TestCreateJobDedupPullForwardWakesRunner is spec 2026-09-25-17 Tests item 1:
// pulling an already-queued job earlier through CreateJob's dedup path
// (findAndUpdateExistingJob) must wake a GetJobWait runner that is sleeping on
// a DIFFERENT, later-due job — exactly like the brand-new-insert path
// (createNewJob) already does. Before the fix, the dedup path never called
// notifier.Notify at all, so the pulled-forward job only ran once the
// runner's existing timer eventually fired.
func TestCreateJobDedupPullForwardWakesRunner(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc := newTestJobService(t)

	// Job A: what GetJobWait ends up sleeping on. Its due time is the ceiling
	// this test proves the dedup-pulled job beats — generous enough (4s) not
	// to be a wall-clock landmine, short enough to keep the test fast.
	jobA := models.NewJob(nil, "wake_dedup_a")
	jobA.Status = models.JobStatusPending
	jobA.ScheduledAt = time.Now().Add(4 * time.Second)
	_, err := dbSvc.DB().NewInsert().Model(jobA).Exec(ctx)
	r.NoError(err)

	// Job B: queued far out, empty config. A later CreateJob call with the
	// same type + config hits findAndUpdateExistingJob (the dedup path)
	// instead of inserting a new row.
	farAt := time.Now().Add(20 * time.Second)
	jobB, err := svc.CreateJob(ctx, "", "wake_dedup_b", []byte(`{}`), &jobsvc.JobOptions{ScheduledAt: &farAt})
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

	// Let GetJobWait claim-miss on both A and B, subscribe to job.created and
	// settle into its wait (timed off A, the earliest pending job) before the
	// dedup pull-forward below — otherwise the wake-up notification would fire
	// before anyone is listening for it.
	time.Sleep(200 * time.Millisecond)

	// Same type + config as jobB, nil options ("schedule now"): hits the dedup
	// path and pulls B's scheduled_at from 20s out to now.
	pullTime := time.Now()

	pulled, err := svc.CreateJob(ctx, "", "wake_dedup_b", []byte(`{}`), nil)
	r.NoError(err)
	r.Equal(jobB.UID, pulled.UID,
		"must be the dedup path updating the SAME row — a different UID means this went through "+
			"the insert path instead and proves nothing about the dedup fix")

	// Confirm there is exactly one row of type wake_dedup_b: further proof the
	// dedup path updated the existing row rather than a second one being
	// inserted alongside it. Deliberately not filtered by status: the wake-up
	// racing this very check may already have let GetJobWait claim it
	// (pending -> running) by the time we look.
	count, err := dbSvc.DB().NewSelect().
		Model((*models.Job)(nil)).
		Where("type = ?", "wake_dedup_b").
		Where("deleted_at IS NULL").
		Count(ctx)
	r.NoError(err)
	r.Equal(1, count, "dedup must update the existing row in place, never insert a second one")

	select {
	case res := <-resultCh:
		r.NoError(res.err)
		r.NotNil(res.job)
		r.Equal(jobB.UID, res.job.UID, "must claim the dedup-pulled job B, not job A")
		r.Lessf(time.Since(pullTime), 2*time.Second,
			"the dedup-pulled job should be claimed almost immediately via the wake-up notify, "+
				"well before A's 4s timer would otherwise fire")
	case <-time.After(6 * time.Second):
		t.Fatal("GetJobWait did not return within 6s of the dedup pull-forward")
	}
}

// TestCreateJobDedupPushLaterDoesNotNotify is spec 2026-09-25-17 Tests item 2:
// bouncing an already-queued job LATER through the dedup path must not emit
// job.created — that would wake every sleeping runner for a change that made
// nothing more claimable (the job-storm guard in CreateJob's found branch).
func TestCreateJobDedupPushLaterDoesNotNotify(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, counting := newTestJobServiceWithCountingNotifier(t)

	originalAt := time.Now().Add(30 * time.Second)
	job, err := svc.CreateJob(ctx, "", "wake_dedup_push_later", []byte(`{}`),
		&jobsvc.JobOptions{ScheduledAt: &originalAt})
	r.NoError(err)

	// Reset: the initial insert is due within the 15-minute window and
	// legitimately notifies once via createNewJob. Only the dedup call below
	// is under test.
	counting.jobCreatedCount.Store(0)

	laterAt := time.Now().Add(5 * time.Minute)
	updated, err := svc.CreateJob(ctx, "", "wake_dedup_push_later", []byte(`{}`),
		&jobsvc.JobOptions{ScheduledAt: &laterAt})
	r.NoError(err)
	r.Equal(job.UID, updated.UID, "must be the dedup path, not a fresh insert")

	r.Zero(counting.jobCreatedCount.Load(),
		"pushing an already-queued job LATER must not notify — nothing became more claimable")
}

// TestCreateJobDedupPullEarlierWithinWindowNotifies is spec 2026-09-25-17
// Tests item 3, the positive control for item 2: the same setup, but pulling
// the job EARLIER (and still inside the 15-minute wake window) must emit
// exactly one job.created. Without this, item 2's assertion of zero notifies
// would prove nothing — the counter could simply be unable to see a notify at
// all.
func TestCreateJobDedupPullEarlierWithinWindowNotifies(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, counting := newTestJobServiceWithCountingNotifier(t)

	originalAt := time.Now().Add(30 * time.Second)
	job, err := svc.CreateJob(ctx, "", "wake_dedup_pull_earlier", []byte(`{}`),
		&jobsvc.JobOptions{ScheduledAt: &originalAt})
	r.NoError(err)

	counting.jobCreatedCount.Store(0)

	earlierAt := time.Now().Add(5 * time.Second)
	updated, err := svc.CreateJob(ctx, "", "wake_dedup_pull_earlier", []byte(`{}`),
		&jobsvc.JobOptions{ScheduledAt: &earlierAt})
	r.NoError(err)
	r.Equal(job.UID, updated.UID, "must be the dedup path, not a fresh insert")

	r.EqualValues(1, counting.jobCreatedCount.Load(),
		"pulling an already-queued job EARLIER, inside the wake window, must notify exactly once")
}

// TestCreateJobDedupPullEarlierBeyondWindowDoesNotNotify is spec
// 2026-09-25-17 Tests item 4: pulling a job earlier through the dedup path
// still must not notify when the new time lands beyond the 15-minute wake
// window — the same gate createNewJob applies to a brand-new row. A sleeping
// runner will pick it up via nextPendingWait once it is closer to due; there
// is nothing to wake it up for yet.
func TestCreateJobDedupPullEarlierBeyondWindowDoesNotNotify(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, counting := newTestJobServiceWithCountingNotifier(t)

	// 2h out: beyond the wake window, so the initial insert itself must not
	// notify either — no reset needed before the assertion below.
	originalAt := time.Now().Add(2 * time.Hour)
	job, err := svc.CreateJob(ctx, "", "wake_dedup_beyond_window", []byte(`{}`),
		&jobsvc.JobOptions{ScheduledAt: &originalAt})
	r.NoError(err)
	r.Zero(counting.jobCreatedCount.Load(), "a job 2h out must not notify on insert either")

	// Pulled to 30 minutes out: earlier than before, but still outside the
	// 15-minute window.
	stillFarAt := time.Now().Add(30 * time.Minute)
	updated, err := svc.CreateJob(ctx, "", "wake_dedup_beyond_window", []byte(`{}`),
		&jobsvc.JobOptions{ScheduledAt: &stillFarAt})
	r.NoError(err)
	r.Equal(job.UID, updated.UID, "must be the dedup path, not a fresh insert")

	r.Zero(counting.jobCreatedCount.Load(),
		"pulling a job earlier but still beyond the 15-minute wake window must not notify")
}
