package testsupport

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/uptrace/bun"
)

// FaultHook is a bun query hook that fails one chosen query by handing the
// driver an already canceled context. It lets a test replay one happy-path
// scenario once per query it issues, failing a different query each time, so
// every error branch along the path is executed without closing the pool.
type FaultHook struct {
	seen   atomic.Int64
	failAt atomic.Int64
	armed  atomic.Bool
}

// InstallFaults registers a FaultHook on the database. The hook is inert until
// a scenario arms it.
func InstallFaults(db *bun.DB) *FaultHook {
	hook := &FaultHook{}
	db.AddQueryHook(hook)

	return hook
}

// BeforeQuery implements bun.QueryHook.
func (h *FaultHook) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	if !h.armed.Load() {
		return ctx
	}

	n := h.seen.Add(1)
	if n != h.failAt.Load() {
		return ctx
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	return canceled
}

// AfterQuery implements bun.QueryHook.
func (h *FaultHook) AfterQuery(context.Context, *bun.QueryEvent) {}

// MaxFaults caps how many queries of one scenario are failed in turn.
const MaxFaults = 40

// Sweep runs scenario once to count the queries issued after it calls arm,
// then once more per counted query with that query failing. The scenario
// seeds whatever it needs, calls arm right before the operation under test,
// and must tolerate that operation failing (it must not assert success).
// A scenario is replayed on the same database, so it must be repeatable.
func (h *FaultHook) Sweep(t *testing.T, scenario func(arm func())) {
	t.Helper()

	run := func(failAt int64) {
		h.seen.Store(0)
		h.failAt.Store(failAt)
		h.armed.Store(false)

		scenario(func() {
			h.seen.Store(0)
			h.armed.Store(true)
		})

		h.armed.Store(false)
	}

	run(0)

	total := min(h.seen.Load(), MaxFaults)
	// seen was reset by arm and counted the operation's queries; run(0) never
	// fails any (failAt 0 never matches a 1-based counter).
	for n := int64(1); n <= total; n++ {
		run(n)
	}
}

// MaxFreshFaults caps how many queries SweepFresh fails in turn.
const MaxFreshFaults = 400

// SweepFresh is Sweep for an operation that cannot be replayed on one database
// (a seeder that skips itself once its data exists). newRun builds a fresh
// database with a FaultHook installed and returns the hook and the operation;
// it is called once to count the operation's queries, then once per query
// with that query failing. The operation must tolerate failing.
func SweepFresh(t *testing.T, newRun func() (*FaultHook, func())) {
	t.Helper()

	hook, operation := newRun()
	hook.armed.Store(true)
	operation()

	total := min(hook.seen.Load(), MaxFreshFaults)
	for n := int64(1); n <= total; n++ {
		hook, operation = newRun()
		hook.failAt.Store(n)
		hook.armed.Store(true)
		operation()
	}
}
