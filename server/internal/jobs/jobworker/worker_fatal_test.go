package jobworker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/dbfault"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// errMissingJobsTable is the incident verbatim: the pure-Go SQLite driver's
// wording for a table that is no longer there.
var errMissingJobsTable = errors.New("SQL logic error: no such table: jobs (1)")

// runLoopUntilDone runs one worker loop and reports whether it returned before
// deadline. No wall-clock assertions: the test either observes the runner exit
// or fails on a generous timeout.
func runLoopUntilDone(ctx context.Context, t *testing.T, worker *JobWorker) bool {
	t.Helper()

	done := make(chan struct{})

	worker.wg.Add(1)

	go func() {
		defer close(done)
		worker.workerLoop(ctx, 0)
	}()

	select {
	case <-done:
		return true
	case <-time.After(10 * time.Second):
		return false
	}
}

// TestStructuralFaultStopsTheRunnerAndFiresTheLatch is the core of the fix: a
// missing table must terminate, not retry. Before this, the same loop retried
// the same impossible query for seventeen hours.
func TestStructuralFaultStopsTheRunnerAndFiresTheLatch(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rec := &loopRecorder{}
	svc := failingSvc(rec, errMissingJobsTable)
	worker := newLoopTestWorker(svc, rec)

	logs := &bytes.Buffer{}
	latch := dbfault.NewLatch(slog.New(slog.NewTextHandler(logs, nil)))

	fired := make(chan struct{}, 1)
	latch.Arm(func() { fired <- struct{}{} })
	worker.SetFaultLatch(latch)

	// A context that is never canceled: only the fault can end this loop.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r.True(runLoopUntilDone(ctx, t, worker), "the runner must stop on a structural fault")

	select {
	case <-fired:
	default:
		r.Fail("the terminal action must have been triggered")
	}

	fault := latch.Fault()
	r.NotNil(fault)
	r.Equal(dbfault.BackendSQLite, fault.Backend)
	r.Equal("undefined_table", fault.Reason)

	// One clear line naming the fault — not an undifferentiated stream of
	// query errors.
	r.Equal(1, strings.Count(logs.String(), dbfault.LogMessage))
	r.Contains(logs.String(), "reason=undefined_table")
	r.Contains(logs.String(), "component=job_worker")

	// It gave up immediately: it never entered the retry backoff, and it never
	// logged the ordinary "Error processing job" line for this.
	r.Empty(rec.waits(), "a structural fault must not be backed off and retried")
	r.Empty(rec.logs(msgProcessingError))
	r.Equal(1, countCalls(rec), "exactly one attempt, then stop")
}

// TestTransientErrorNeverKillsTheProcess is the regression guard: the whole
// point is to terminate on structural faults ONLY. A dropped connection must
// still retry, back off, and recover, exactly as before.
func TestTransientErrorNeverKillsTheProcess(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rec := &loopRecorder{}
	worker := newLoopTestWorker(nil, rec)

	logs := &bytes.Buffer{}
	latch := dbfault.NewLatch(slog.New(slog.NewTextHandler(logs, nil)))

	var fired int
	latch.Arm(func() { fired++ })
	worker.SetFaultLatch(latch)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Fail transiently a few times, then succeed: the runner must survive the
	// outage and report recovery.
	const failures = 4

	worker.jobSvc = &fakeJobSvc{waitFn: func(context.Context) (*models.Job, error) {
		rec.add(loopEvent{kind: eventCall})

		if countCalls(rec) <= failures {
			return nil, &pq.Error{Code: "08006", Message: "server closed the connection unexpectedly"}
		}

		return successJob(), nil
	}}

	// Stop the loop once it has recovered.
	rec.cancelOnLog(msgRecovered, cancel)
	worker.backoffAfter = rec.instantAfter(100, cancel)

	r.True(runLoopUntilDone(ctx, t, worker))

	r.Zero(fired, "a transient error must never trigger the terminal action")
	r.Nil(latch.Fault())
	r.Empty(logs.String(), "a transient error must not produce a structural-fault line")
	r.NotEmpty(rec.waits(), "…it backs off as before")
	r.NotEmpty(rec.logs(msgRecovered), "…and recovers without killing the process")
}

func countCalls(rec *loopRecorder) int {
	n := 0

	for _, ev := range rec.snapshot() {
		if ev.kind == eventCall {
			n++
		}
	}

	return n
}
