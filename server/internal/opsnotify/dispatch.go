package opsnotify

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// DispatchFunc hands a notice off for asynchronous delivery. The wiring in
// app/server.go enqueues an `operator_notice` job; a test substitutes a stub.
type DispatchFunc func(ctx context.Context, notice Notice) error

// dispatcher is the installed hand-off, or nil on an instance that never wired
// one (every unit test that does not care, and any process running without a
// job service).
var dispatcher atomic.Pointer[DispatchFunc] //nolint:gochecknoglobals // deliberate process-wide hook, see Notify

// SetDispatcher installs the hand-off. Passing nil uninstalls it, which is how
// a test restores the previous state.
//
// A package-level hook rather than a service dependency for exactly the reason
// analytics.Capture is one: the raisers are `internal/support` and
// `handlers/auth`, both of which sit UNDER `app/services` in the import graph
// (services.Registry holds a *support.Service). Threading a delivery service
// down to them would close an import cycle; a hook installed at boot does not.
func SetDispatcher(fn DispatchFunc) {
	if fn == nil {
		dispatcher.Store(nil)

		return
	}

	dispatcher.Store(&fn)
}

// Notify raises a notice. It is fire-and-forget and CANNOT fail the caller.
//
// This is load-bearing, not defensive coding: the two raisers are a support
// webhook answering a provider under a deadline and a signup completing. A
// signup must never fail because Telegram is down, and a webhook must never
// time out because the job table is busy. Every error is logged and metered
// instead — a silent drop stays visible in solidping_operator_notice_total.
func Notify(ctx context.Context, notice Notice) {
	fn := dispatcher.Load()
	if fn == nil {
		return
	}

	defer func() {
		if rec := recover(); rec != nil {
			count(notice.Event, contactTypeNone, outcomeFailed)
			slog.Default().WarnContext(ctx, "Operator notice dispatch panicked",
				"event", notice.Event, "panic", fmt.Sprint(rec))
		}
	}()

	if err := (*fn)(ctx, notice); err != nil {
		count(notice.Event, contactTypeNone, outcomeFailed)
		slog.Default().WarnContext(ctx, "Failed to enqueue an operator notice",
			"event", notice.Event, "error", err)

		return
	}

	prommetrics.OperatorNotice.WithLabelValues(notice.Event, contactTypeNone, "enqueued").Inc()
}
