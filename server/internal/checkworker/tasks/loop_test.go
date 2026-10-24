package tasks_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/tasks"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// fakeClient hands out one claim, then nothing (or claimErr), and records
// every report.
type fakeClient struct {
	mu           sync.Mutex
	claim        *workertasks.Claim
	claimErr     error
	claims       atomic.Int32
	heartbeatErr error
	fails        []string
	finished     atomic.Int32
}

func (c *fakeClient) Claim(_ context.Context, _ []string) (*workertasks.Claim, error) {
	c.claims.Add(1)

	c.mu.Lock()
	defer c.mu.Unlock()

	claim := c.claim
	c.claim = nil

	if claim == nil {
		return nil, c.claimErr
	}

	return claim, nil
}

func (c *fakeClient) Heartbeat(_ context.Context, _ string) error { return c.heartbeatErr }

func (c *fakeClient) Step(_ context.Context, _ string, _ workertasks.StepRequest) error { return nil }

func (c *fakeClient) Finish(_ context.Context, _ string, _ json.RawMessage) error {
	c.finished.Add(1)

	return nil
}

func (c *fakeClient) Fail(_ context.Context, _ string, code string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.fails = append(c.fails, code)

	return nil
}

func (c *fakeClient) failCodes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.fails...)
}

type fakeExecutor struct {
	run func(ctx context.Context, claim *workertasks.Claim, rep tasks.Reporter) error
}

func (fakeExecutor) Kind() string       { return "fake" }
func (fakeExecutor) Capability() string { return "fake-cap" }

func (e fakeExecutor) Run(ctx context.Context, claim *workertasks.Claim, rep tasks.Reporter) error {
	return e.run(ctx, claim, rep)
}

func newLoop(client tasks.Client, executor tasks.Executor) *tasks.Loop {
	return &tasks.Loop{
		Client:         client,
		Executors:      []tasks.Executor{executor},
		Slots:          1,
		IdleDelay:      10 * time.Millisecond,
		HeartbeatEvery: 5 * time.Millisecond,
	}
}

// runFor runs the loop until done is closed or the timeout, then stops it.
func runFor(t *testing.T, loop *tasks.Loop, done <-chan struct{}) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	stopped := make(chan struct{})

	go func() {
		loop.Run(ctx)
		close(stopped)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("loop did not reach the expected state")
	}

	cancel()
	<-stopped
}

func TestLoopHeartbeat410CancelsRun(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	client := &fakeClient{claim: &workertasks.Claim{UID: "t1", Kind: "fake"}, heartbeatErr: workertasks.ErrTaskGone}
	done := make(chan struct{})

	executor := fakeExecutor{run: func(ctx context.Context, _ *workertasks.Claim, _ tasks.Reporter) error {
		<-ctx.Done() // only the 410 can end this
		close(done)

		return ctx.Err()
	}}

	runFor(t, newLoop(client, executor), done)

	r.Empty(client.failCodes(), "a gone task is not reported as failed")
}

func TestLoopPanicBecomesFailSlice(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	client := &fakeClient{claim: &workertasks.Claim{UID: "t1", Kind: "fake"}}
	done := make(chan struct{})

	var once sync.Once

	executor := fakeExecutor{run: func(_ context.Context, _ *workertasks.Claim, _ tasks.Reporter) error {
		once.Do(func() { close(done) })

		panic("boom")
	}}

	runFor(t, newLoop(client, executor), done)

	r.Eventually(func() bool { return len(client.failCodes()) == 1 }, time.Second, 5*time.Millisecond)
	r.Equal([]string{"panic"}, client.failCodes())
}

func TestLoopRunSeesEgressGuardAndReports(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	client := &fakeClient{claim: &workertasks.Claim{UID: "t1", Kind: "fake"}}
	guard := egress.New(false)

	done := make(chan struct{})

	var seen atomic.Pointer[egress.Guard]

	executor := fakeExecutor{run: func(ctx context.Context, _ *workertasks.Claim, rep tasks.Reporter) error {
		seen.Store(egress.FromContext(ctx))
		defer close(done)

		return rep.Finish(ctx, json.RawMessage(`{}`))
	}}

	loop := newLoop(client, executor)
	loop.Guard = guard

	runFor(t, loop, done)

	r.Same(guard, seen.Load())
	r.EqualValues(1, client.finished.Load())
	r.Empty(client.failCodes())
}

func TestLoopNoReportFailsSlice(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	client := &fakeClient{claim: &workertasks.Claim{UID: "t1", Kind: "fake"}}
	done := make(chan struct{})

	executor := fakeExecutor{run: func(_ context.Context, _ *workertasks.Claim, _ tasks.Reporter) error {
		close(done)

		return nil
	}}

	runFor(t, newLoop(client, executor), done)

	r.Eventually(func() bool { return len(client.failCodes()) == 1 }, time.Second, 5*time.Millisecond)
	r.Equal([]string{"no_report"}, client.failCodes())
}

// An old server answers 404: the loop sleeps and claims again, it never stops.
func TestLoopOldServerSleeps(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	client := &fakeClient{claimErr: tasks.ErrNotSupported}
	done := make(chan struct{})

	go func() {
		for client.claims.Load() < 3 {
			time.Sleep(5 * time.Millisecond)
		}

		close(done)
	}()

	executor := fakeExecutor{run: func(context.Context, *workertasks.Claim, tasks.Reporter) error {
		r.Fail("never claimed")

		return nil
	}}

	started := time.Now()
	runFor(t, newLoop(client, executor), done)

	r.GreaterOrEqual(time.Since(started), 20*time.Millisecond, "it slept between claims")
}

func TestRegistryCapabilities(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	tasks.Register(fakeExecutor{})
	r.Contains(tasks.Capabilities(), "fake-cap")
}
