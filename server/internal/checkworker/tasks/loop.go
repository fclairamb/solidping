package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// Defaults of the loop.
const (
	// DefaultIdleDelay is the sleep after a claim that returned nothing, a 404
	// from an old server, or an error.
	DefaultIdleDelay = 15 * time.Second
	// DefaultHeartbeatEvery is the heartbeat period while Run works. The lease
	// is 2 minutes, so three heartbeats can fail before it is lost.
	DefaultHeartbeatEvery = 30 * time.Second
	// reportTimeout bounds one report sent after Run returned.
	reportTimeout = 30 * time.Second
)

// Failure codes the loop reports when the executor did not.
const (
	codePanic    = "panic"
	codeError    = "executor_error"
	codeNoReport = "no_report"
)

var errPanic = errors.New("executor panicked")

// Loop claims tasks for its executors and runs them.
type Loop struct {
	Client    Client
	Executors []Executor
	// Slots is how many tasks each executor may run at once (the server
	// enforces the worker's total).
	Slots int
	// Guard is the worker's egress policy, put on every Run context.
	Guard *egress.Guard
	// Logger receives the loop's lines.
	Logger *slog.Logger
	// InfoLogs logs one INFO line per task (agents: the log is their only
	// record, they have no database). Otherwise those lines are DEBUG.
	InfoLogs bool
	// IdleDelay and HeartbeatEvery default to DefaultIdleDelay and
	// DefaultHeartbeatEvery.
	IdleDelay      time.Duration
	HeartbeatEvery time.Duration
}

// Run starts one claim loop per executor slot and blocks until ctx is done.
func (l *Loop) Run(ctx context.Context) {
	if l.Logger == nil {
		l.Logger = slog.Default().With("component", "worker_tasks")
	}

	if l.IdleDelay <= 0 {
		l.IdleDelay = DefaultIdleDelay
	}

	if l.HeartbeatEvery <= 0 {
		l.HeartbeatEvery = DefaultHeartbeatEvery
	}

	slots := max(l.Slots, 1)

	var group sync.WaitGroup

	for _, executor := range l.Executors {
		for range slots {
			group.Go(func() { l.claimLoop(ctx, executor) })
		}
	}

	group.Wait()
}

func (l *Loop) claimLoop(ctx context.Context, executor Executor) {
	kinds := []string{executor.Kind()}

	for ctx.Err() == nil {
		claim, err := l.Client.Claim(ctx, kinds)

		switch {
		case err != nil && !errors.Is(err, ErrNotSupported) && ctx.Err() == nil:
			l.Logger.WarnContext(ctx, "Worker task claim failed", "kind", executor.Kind(), "error", err)
		case err == nil && claim != nil:
			l.runClaim(ctx, executor, claim)

			continue
		}

		l.sleep(ctx)
	}
}

// sleep waits IdleDelay plus up to 20% jitter, so agents restarted together
// do not claim in lockstep.
func (l *Loop) sleep(ctx context.Context) {
	delay := l.IdleDelay + time.Duration(rand.Int64N(int64(l.IdleDelay)/5+1))

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// runClaim runs one slice: heartbeats while Run works, cancels Run when the
// task is gone, and fails the slice when Run panicked, errored or returned
// without a report.
func (l *Loop) runClaim(ctx context.Context, executor Executor, claim *workertasks.Claim) {
	started := time.Now()

	runCtx := ctx
	if l.Guard != nil {
		runCtx = egress.WithGuard(runCtx, l.Guard)
	}

	runCtx, cancel := context.WithCancel(runCtx)
	defer cancel()

	rep := &reporter{client: l.Client, uid: claim.UID}

	heartbeatDone := make(chan struct{})

	go func() {
		defer close(heartbeatDone)
		l.heartbeat(runCtx, cancel, rep)
	}()

	runErr := safeRun(runCtx, executor, claim, rep)

	cancel()
	<-heartbeatDone

	outcome := rep.outcome()

	if outcome == "" && !rep.gone.Load() {
		code := codeNoReport

		switch {
		case errors.Is(runErr, errPanic):
			code = codePanic
		case runErr != nil:
			code = codeError
		}

		reportCtx, reportCancel := context.WithTimeout(context.WithoutCancel(ctx), reportTimeout)
		if err := l.Client.Fail(reportCtx, claim.UID, code); err != nil && !errors.Is(err, workertasks.ErrTaskGone) {
			l.Logger.WarnContext(ctx, "Worker task failure report failed", "task", claim.UID, "error", err)
		}

		reportCancel()

		outcome = "fail:" + code
	}

	if rep.gone.Load() {
		outcome = "gone"
	}

	attrs := []any{
		"task", claim.UID, "kind", claim.Kind, "slice", claim.Slice,
		"attempt", claim.Attempt, "duration", time.Since(started).Round(time.Millisecond), "outcome", outcome,
	}

	if runErr != nil || outcome == "gone" || rep.failed.Load() {
		if runErr != nil {
			attrs = append(attrs, "error", runErr)
		}

		l.Logger.WarnContext(ctx, "Worker task slice failed", attrs...)

		return
	}

	level := slog.LevelDebug
	if l.InfoLogs {
		level = slog.LevelInfo
	}

	l.Logger.Log(ctx, level, "Worker task slice done", attrs...)
}

func (l *Loop) heartbeat(ctx context.Context, cancel context.CancelFunc, rep *reporter) {
	ticker := time.NewTicker(l.HeartbeatEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := rep.Heartbeat(ctx)
			if errors.Is(err, workertasks.ErrTaskGone) {
				l.Logger.WarnContext(ctx, "Worker task gone, stopping it", "task", rep.uid)
				cancel()

				return
			}

			if err != nil && ctx.Err() == nil {
				l.Logger.WarnContext(ctx, "Worker task heartbeat failed", "task", rep.uid, "error", err)
			}
		}
	}
}

func safeRun(ctx context.Context, executor Executor, claim *workertasks.Claim, rep Reporter) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: %v", errPanic, recovered)
		}
	}()

	return executor.Run(ctx, claim, rep)
}

// reporter binds a Client to one task and remembers how the slice ended.
type reporter struct {
	client Client
	uid    string

	mu     sync.Mutex
	ended  string
	gone   atomic.Bool
	failed atomic.Bool
}

func (r *reporter) outcome() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.ended
}

func (r *reporter) track(err error, outcome string) error {
	if errors.Is(err, workertasks.ErrTaskGone) {
		r.gone.Store(true)

		return err
	}

	if err == nil {
		r.mu.Lock()
		r.ended = outcome
		r.mu.Unlock()
	}

	return err
}

func (r *reporter) Heartbeat(ctx context.Context) error {
	err := r.client.Heartbeat(ctx, r.uid)
	if errors.Is(err, workertasks.ErrTaskGone) {
		r.gone.Store(true)
	}

	return err
}

func (r *reporter) Step(ctx context.Context, req workertasks.StepRequest) error {
	return r.track(r.client.Step(ctx, r.uid, req), "step")
}

func (r *reporter) Finish(ctx context.Context, body json.RawMessage) error {
	return r.track(r.client.Finish(ctx, r.uid, body), "finish")
}

func (r *reporter) Fail(ctx context.Context, errorCode string) error {
	err := r.track(r.client.Fail(ctx, r.uid, errorCode), "fail:"+errorCode)
	if err == nil {
		r.failed.Store(true)
	}

	return err
}
