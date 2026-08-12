package procwatch_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/procwatch"
)

// waitFor blocks until cond holds or the (generous) budget runs out. Nothing
// here asserts on wall-clock durations — the interval is only there to make the
// poll fast.
func waitFor(t *testing.T, cond func() bool) bool {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}

		time.Sleep(time.Millisecond)
	}

	return false
}

func TestOrphanedProcessTriggersShutdown(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var ppid atomic.Int64
	ppid.Store(4242)

	fired := make(chan struct{})
	watcher := procwatch.ParentWatcher{
		Interval: time.Millisecond,
		PPID:     func() int { return int(ppid.Load()) },
	}

	go watcher.Run(t.Context(), func() { close(fired) })

	// While the parent lives, nothing happens.
	select {
	case <-fired:
		r.Fail("must not fire while the parent is alive")
	case <-time.After(20 * time.Millisecond):
	}

	// The parent dies: we are adopted by init.
	ppid.Store(1)

	select {
	case <-fired:
	case <-time.After(10 * time.Second):
		r.Fail("must shut down once the parent is gone")
	}
}

func TestWatcherStopsWithItsContext(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, cancel := context.WithCancel(context.Background())

	var fired atomic.Bool

	done := make(chan struct{})

	go func() {
		defer close(done)

		procwatch.ParentWatcher{
			Interval: time.Millisecond,
			PPID:     func() int { return 4242 },
		}.Run(ctx, func() { fired.Store(true) })
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		r.Fail("Run must return when its context is canceled")
	}

	r.False(fired.Load())
}

func TestAlreadyParentlessProcessIsNotWatched(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// A container entrypoint's parent is already 1: there is nothing to
	// outlive, and the watcher must not read that as "orphaned".
	var fired atomic.Bool

	done := make(chan struct{})

	go func() {
		defer close(done)

		procwatch.ParentWatcher{
			Interval: time.Millisecond,
			PPID:     func() int { return 1 },
		}.Run(t.Context(), func() { fired.Store(true) })
	}()

	r.True(waitFor(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}), "Run must return immediately when there is no parent")

	r.False(fired.Load())
}
