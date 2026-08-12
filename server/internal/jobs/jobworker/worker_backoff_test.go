package jobworker

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/stats"
)

// errBoom is the persistent, instantly-returned infrastructure error the
// original incident was made of (a missing table failing in microseconds).
var errBoom = errors.New("no such table: jobs")

// Log messages the runner emits around failures; asserted verbatim.
const (
	msgProcessingError = "Error processing job"
	msgRecovered       = "Job processing recovered"
)

// Event kinds recorded by loopRecorder, in the order they happen.
const (
	eventCall = "call" // processNext asked the service for a job
	eventLog  = "log"  // a log record was emitted
	eventWait = "wait" // the runner entered a backoff wait
)

type loopEvent struct {
	kind  string
	msg   string         // log message (eventLog)
	attrs map[string]any // log attributes (eventLog)
	delay time.Duration  // requested backoff (eventWait)
}

// loopRecorder captures calls, log records and backoff waits into a single
// ordered timeline, so a test can assert not just what happened but in which
// order (e.g. that the first failure is logged before anything sleeps).
type loopRecorder struct {
	mu     sync.Mutex
	events []loopEvent
	onLog  map[string]func() // fired after a log record with that message
}

func (rec *loopRecorder) add(ev loopEvent) {
	rec.mu.Lock()
	rec.events = append(rec.events, ev)
	hook := rec.onLog[ev.msg]
	rec.mu.Unlock()

	if ev.kind == eventLog && hook != nil {
		hook()
	}
}

func (rec *loopRecorder) cancelOnLog(msg string, cancel context.CancelFunc) {
	rec.mu.Lock()
	defer rec.mu.Unlock()

	if rec.onLog == nil {
		rec.onLog = map[string]func(){}
	}

	rec.onLog[msg] = func() { cancel() }
}

func (rec *loopRecorder) snapshot() []loopEvent {
	rec.mu.Lock()
	defer rec.mu.Unlock()

	return slices.Clone(rec.events)
}

// timeline keeps only the events this spec is about: service calls, backoff
// waits, and the two failure/recovery log lines. Unrelated chatter ("Job runner
// started", "Processing job", …) is dropped.
func (rec *loopRecorder) timeline() []loopEvent {
	var out []loopEvent

	for _, ev := range rec.snapshot() {
		if ev.kind == eventLog && ev.msg != msgProcessingError && ev.msg != msgRecovered {
			continue
		}

		out = append(out, ev)
	}

	return out
}

func (rec *loopRecorder) waits() []time.Duration {
	var out []time.Duration

	for _, ev := range rec.snapshot() {
		if ev.kind == eventWait {
			out = append(out, ev.delay)
		}
	}

	return out
}

func (rec *loopRecorder) logs(msg string) []loopEvent {
	var out []loopEvent

	for _, ev := range rec.snapshot() {
		if ev.kind == eventLog && ev.msg == msg {
			out = append(out, ev)
		}
	}

	return out
}

// instantAfter records the requested backoff and returns an already-elapsed
// timer, so the loop runs at full speed while the delays it *asked* for stay
// observable — no wall-clock sleeping, nothing to flake on. Once stopAfter
// waits have been recorded it cancels the context and returns a channel that
// never fires, so the loop leaves through its ctx.Done() branch.
func (rec *loopRecorder) instantAfter(stopAfter int, cancel context.CancelFunc) func(time.Duration) <-chan time.Time {
	return func(delay time.Duration) <-chan time.Time {
		rec.add(loopEvent{kind: eventWait, delay: delay})

		if len(rec.waits()) >= stopAfter {
			cancel()

			return make(chan time.Time)
		}

		elapsed := make(chan time.Time, 1)
		elapsed <- time.Now()

		return elapsed
	}
}

// captureHandler is a slog.Handler feeding a loopRecorder.
type captureHandler struct {
	rec  *loopRecorder
	base []slog.Attr
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, record slog.Record) error {
	attrs := map[string]any{}
	for _, attr := range h.base {
		attrs[attr.Key] = attr.Value.Any()
	}

	record.Attrs(func(attr slog.Attr) bool {
		attrs[attr.Key] = attr.Value.Any()

		return true
	})

	h.rec.add(loopEvent{kind: eventLog, msg: record.Message, attrs: attrs})

	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &captureHandler{rec: h.rec, base: append(slices.Clip(h.base), attrs...)}
}

func (h *captureHandler) WithGroup(_ string) slog.Handler { return h }

// newLoopTestWorker builds a worker whose logger feeds the recorder.
func newLoopTestWorker(svc *fakeJobSvc, rec *loopRecorder) *JobWorker {
	logger := slog.New(&captureHandler{rec: rec})

	return &JobWorker{
		jobSvc:              svc,
		logger:              logger,
		stats:               stats.NewProcessingStats(time.Minute, time.Minute, logger),
		cancelWatchInterval: time.Hour, // no row polling in these tests
	}
}

// failingSvc returns a service whose every call fails instantly, like a
// dropped connection or a missing table.
func failingSvc(rec *loopRecorder) *fakeJobSvc {
	return &fakeJobSvc{waitFn: func(context.Context) (*models.Job, error) {
		rec.add(loopEvent{kind: eventCall})

		return nil, errBoom
	}}
}

// scriptedSvc replays outcomes: true = a job the real processNext completes
// successfully, false = an instant failure. Calls past the script fail.
func scriptedSvc(rec *loopRecorder, outcomes []bool) *fakeJobSvc {
	var (
		mu   sync.Mutex
		next int
	)

	return &fakeJobSvc{waitFn: func(context.Context) (*models.Job, error) {
		mu.Lock()
		index := next
		next++
		mu.Unlock()

		rec.add(loopEvent{kind: eventCall})

		if index < len(outcomes) && outcomes[index] {
			return successJob(), nil
		}

		return nil, errBoom
	}}
}

// successJob is a job the real processNext runs to completion without touching
// the database: the sleep runner with a negative duration returns at once.
func successJob() *models.Job {
	job := models.NewJob(nil, string(jobdef.JobTypeSleep))
	job.Config = models.JSONMap{"seconds": -1}

	return job
}

// runLoop runs one runner to completion, failing the test if it does not stop.
func runLoop(t *testing.T, w *JobWorker, loop func()) {
	t.Helper()

	done := make(chan struct{})

	w.wg.Add(1)

	go func() {
		defer close(done)
		loop()
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "workerLoop did not return")
	}
}

// expectedBackoffs is the delay the loop should ask for after the 1st, 2nd, …
// consecutive failure: doubling from the minimum, saturating at the cap.
func expectedBackoffs(count int) []time.Duration {
	out := make([]time.Duration, 0, count)
	delay := errBackoffMinDelay

	for range count {
		out = append(out, delay)

		if delay >= errBackoffMaxDelay/2 {
			delay = errBackoffMaxDelay
		} else {
			delay *= 2
		}
	}

	return out
}

// TestWorkerLoopBacksOffAndThrottlesLogs is the core regression test for the
// incident: a persistent, instantly-returned error must space out retries up to
// the 30 s cap and must not log every single failure. It also pins the
// operator-facing guarantee that the *first* failure is logged before anything
// sleeps.
func TestWorkerLoopBacksOffAndThrottlesLogs(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const failures = 20

	rec := &loopRecorder{}
	ctx, cancel := context.WithCancel(context.Background())

	defer cancel()

	w := newLoopTestWorker(failingSvc(rec), rec)
	w.backoffAfter = rec.instantAfter(failures, cancel)

	runLoop(t, w, func() { w.workerLoop(ctx, 0) })

	// The first failure is reported immediately: call, log, only then a wait.
	timeline := rec.timeline()
	r.GreaterOrEqual(len(timeline), 3)
	r.Equal(eventCall, timeline[0].kind)
	r.Equal(eventLog, timeline[1].kind, "the first failure must be logged before any backoff")
	r.Equal(msgProcessingError, timeline[1].msg)
	r.Equal(int64(1), timeline[1].attrs["consecutive"])
	r.Equal(eventWait, timeline[2].kind)

	// Each wait is the full-jitter form of the expected geometric backoff.
	waits := rec.waits()
	r.Len(waits, failures)

	expected := expectedBackoffs(failures)
	r.Equal(errBackoffMaxDelay, expected[9], "the cap must be reached by the 10th failure")

	for i, delay := range waits {
		r.GreaterOrEqual(delay, expected[i]/2, "wait %d below the jitter floor", i)
		r.LessOrEqual(delay, expected[i], "wait %d above the expected backoff", i)
	}

	// …and once at the cap it stays there.
	for i := 9; i < failures; i++ {
		r.GreaterOrEqual(waits[i], errBackoffMaxDelay/2)
		r.LessOrEqual(waits[i], errBackoffMaxDelay)
	}

	// 20 failures, 5 log lines: the 1st, 2nd, 4th, 8th and 16th.
	logged := rec.logs(msgProcessingError)
	r.Len(logged, 5)

	for i, want := range []int64{1, 2, 4, 8, 16} {
		r.Equal(want, logged[i].attrs["consecutive"])
		r.Equal(errBoom, logged[i].attrs["error"], "the log must carry the underlying error")
	}
}

// TestSustainedFailureLogVolumeIsBounded turns the acceptance criterion
// ("well under 1 MB/h at the default pool size") into arithmetic on the two
// mechanisms, with no wall clock involved: at the cap, full jitter puts every
// retry at least errBackoffMaxDelay/2 apart, and only power-of-two failure counts
// are logged.
func TestSustainedFailureLogVolumeIsBounded(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const (
		poolSize     = 2   // NewJobWorker's default
		bytesPerLine = 512 // generous: the incident's lines were ~130 bytes
	)

	attemptsPerHour := int(time.Hour / (errBackoffMaxDelay / 2))
	r.Equal(240, attemptsPerHour)

	// Worst hour is the first one (most doublings fit in it).
	firstHour := 0

	for i := 1; i <= attemptsPerHour; i++ {
		if shouldLog(i) {
			firstHour++
		}
	}

	r.Equal(8, firstHour, "1, 2, 4, 8, 16, 32, 64, 128")
	r.Less(firstHour*poolSize*bytesPerLine, 1<<20, "sustained output must stay well under 1 MB/h")

	// A later hour is quieter still: at most one doubling lands in it.
	steady := 0

	for i := attemptsPerHour + 1; i <= 2*attemptsPerHour; i++ {
		if shouldLog(i) {
			steady++
		}
	}

	r.LessOrEqual(steady, 1)

	// Even a 17-hour outage (the original one) stays in the tens of lines.
	whole := 0

	for i := 1; i <= 17*attemptsPerHour; i++ {
		if shouldLog(i) {
			whole++
		}
	}

	r.LessOrEqual(whole*poolSize, 30)
}

// TestNextBackoffSaturatesAtCap pins the growth curve itself.
func TestNextBackoffSaturatesAtCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	w := &JobWorker{}

	delay := time.Duration(0)
	previous := time.Duration(0)

	for step := range 30 {
		delay = w.nextBackoff(delay)

		r.GreaterOrEqual(delay, previous, "backoff must never shrink")
		r.LessOrEqual(delay, errBackoffMaxDelay, "backoff must never exceed the cap")

		if step == 0 {
			r.Equal(errBackoffMinDelay, delay)
		}

		if step >= 9 {
			r.Equal(errBackoffMaxDelay, delay, "step %d should be capped", step)
		}

		previous = delay
	}
}

// TestJitterStaysInFullJitterRangeAndVaries verifies the half-to-full range and
// that two runners failing together do not stay in lockstep.
func TestJitterStaysInFullJitterRangeAndVaries(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Zero(jitter(0))

	seen := map[time.Duration]bool{}

	for range 200 {
		delay := jitter(errBackoffMaxDelay)
		r.GreaterOrEqual(delay, errBackoffMaxDelay/2)
		r.LessOrEqual(delay, errBackoffMaxDelay)
		seen[delay] = true
	}

	r.Greater(len(seen), 1, "jitter must spread runners apart")
}

// TestWorkerLoopResetsAfterTransientFailure covers the transient case: one
// failure, then success. The successful attempt must not be delayed by more
// than the minimum backoff, the recovery must be summarized once, and the
// counter must be back to zero so a later isolated failure is logged
// immediately.
func TestWorkerLoopResetsAfterTransientFailure(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	rec := &loopRecorder{}
	ctx, cancel := context.WithCancel(context.Background())

	defer cancel()

	// fail, succeed, fail.
	w := newLoopTestWorker(scriptedSvc(rec, []bool{false, true, false}), rec)
	w.backoffAfter = rec.instantAfter(2, cancel)

	runLoop(t, w, func() { w.workerLoop(ctx, 0) })

	waits := rec.waits()
	r.Len(waits, 2)
	r.LessOrEqual(waits[0], errBackoffMinDelay, "one transient failure must not delay the next job beyond the minimum")
	r.LessOrEqual(waits[1], errBackoffMinDelay, "the backoff must restart from the minimum after a success")

	// Exactly one summary, and it names the single failure.
	recovered := rec.logs(msgRecovered)
	r.Len(recovered, 1)
	r.Equal(int64(1), recovered[0].attrs["consecutive_failures"])

	// Both failures were logged immediately, each as the first of its outage.
	logged := rec.logs(msgProcessingError)
	r.Len(logged, 2)
	r.Equal(int64(1), logged[0].attrs["consecutive"])
	r.Equal(int64(1), logged[1].attrs["consecutive"], "the counter must reset on success")

	// Ordering: the success is attempted right after the single backoff, and
	// nothing sleeps between the recovery and the next attempt.
	kinds := make([]string, 0, len(rec.timeline()))
	for _, ev := range rec.timeline() {
		kinds = append(kinds, ev.kind)
	}

	r.Equal([]string{
		eventCall, eventLog, eventWait, // failure 1, logged, backed off
		eventCall, eventLog, // success, recovery summary
		eventCall, eventLog, eventWait, // failure 2, logged immediately again
	}, kinds)
}

// TestWorkerLoopRecoverySummaryCountsOutage checks the summary line carries the
// consecutive-failure count and the outage duration — the information the
// flood of identical lines used to bury.
func TestWorkerLoopRecoverySummaryCountsOutage(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	rec := &loopRecorder{}
	ctx, cancel := context.WithCancel(context.Background())

	defer cancel()

	// Three failures, then success. Stop as soon as the summary is emitted.
	rec.cancelOnLog(msgRecovered, cancel)

	w := newLoopTestWorker(scriptedSvc(rec, []bool{false, false, false, true}), rec)
	w.backoffAfter = rec.instantAfter(100, cancel)

	runLoop(t, w, func() { w.workerLoop(ctx, 0) })

	recovered := rec.logs(msgRecovered)
	r.Len(recovered, 1, "exactly one summary line per outage")
	r.Equal(int64(3), recovered[0].attrs["consecutive_failures"])
	r.NotEmpty(recovered[0].attrs["outage"], "the summary must carry the outage duration")

	// The three failures produced two lines (1st and 2nd; the 3rd is collapsed).
	logged := rec.logs(msgProcessingError)
	r.Len(logged, 2)
	r.Equal(int64(1), logged[0].attrs["consecutive"])
	r.Equal(int64(2), logged[1].attrs["consecutive"])
}

// parkedRunner starts a runner whose first failure parks it at the 30 s cap on
// a *real* timer, and returns once it is parked.
func parkedRunner(t *testing.T, rec *loopRecorder) (*JobWorker, chan struct{}, context.CancelFunc) {
	t.Helper()

	entered := make(chan time.Duration, 1)
	ctx, cancel := context.WithCancel(context.Background())

	w := newLoopTestWorker(failingSvc(rec), rec)
	w.backoffMin, w.backoffMax = errBackoffMaxDelay, errBackoffMaxDelay // park at the cap at once
	w.backoffAfter = func(delay time.Duration) <-chan time.Time {
		select {
		case entered <- delay:
		default:
		}

		return time.After(delay) // a real timer, at least 15 s out
	}

	done := make(chan struct{})

	w.wg.Add(1)

	go func() {
		defer close(done)
		w.workerLoop(ctx, 0)
	}()

	select {
	case delay := <-entered:
		require.GreaterOrEqual(t, delay, errBackoffMaxDelay/2, "runner should be parked at the cap")
	case <-time.After(10 * time.Second):
		cancel()
		require.FailNow(t, "runner never entered backoff")
	}

	return w, done, cancel
}

// TestWorkerLoopCancellationDuringBackoff verifies a runner parked at the 30 s
// cap still shuts down promptly: the wait is a select on ctx.Done(), never a
// bare sleep.
func TestWorkerLoopCancellationDuringBackoff(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	rec := &loopRecorder{}
	w, done, cancel := parkedRunner(t, rec)

	defer cancel()

	start := time.Now()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		r.FailNow("workerLoop did not return while backing off")
	}

	r.Less(time.Since(start), 5*time.Second, "shutdown must complete well inside the 30 s cap")
	r.Zero(w.availableRunners.Load())
}

// TestBackingOffRunnerIsNotAvailable verifies the availableRunners accounting:
// the counter is decremented before the backoff wait, so queue-depth metrics
// never claim a sleeping runner is ready for work.
func TestBackingOffRunnerIsNotAvailable(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	rec := &loopRecorder{}
	w, done, cancel := parkedRunner(t, rec)

	defer cancel()

	r.Zero(w.availableRunners.Load(), "a backing-off runner must not count as available")

	cancel()
	<-done

	r.Zero(w.availableRunners.Load())
}
