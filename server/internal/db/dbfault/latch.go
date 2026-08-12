package dbfault

import (
	"context"
	"log/slog"
	"sync"
)

// LogMessage is the single line emitted when a structural fault is first seen.
// It is a constant so operators (and tests) have one exact string to grep for,
// distinct from the undifferentiated stream of query errors an ordinary outage
// produces.
const LogMessage = "Structural database fault: the schema this process needs is gone, retrying cannot fix it"

// Latch records the first structural database fault seen in this process and
// runs a one-shot terminal action — in the server, a graceful shutdown.
//
// It is safe for concurrent use and safe to call on a nil receiver: a component
// constructed without a latch (tests, the integration harness, agent mode)
// keeps its previous behavior with no nil checks at the call sites — a nil
// latch reports every error as non-terminal. Callers that want the
// classification without the terminal action use IsStructural directly.
type Latch struct {
	logger *slog.Logger

	mu     sync.Mutex
	fault  *StructuralError
	action func()
}

// NewLatch returns a latch that logs through logger (slog.Default() when nil).
func NewLatch(logger *slog.Logger) *Latch {
	if logger == nil {
		logger = slog.Default()
	}

	return &Latch{logger: logger}
}

// Arm installs the terminal action. If a fault was already latched — a runner
// can fail before the server finishes wiring itself up — the action runs
// immediately, so the fault can never be swallowed by a startup race.
func (l *Latch) Arm(action func()) {
	if l == nil || action == nil {
		return
	}

	l.mu.Lock()
	l.action = action
	alreadyFaulted := l.fault != nil
	l.mu.Unlock()

	if alreadyFaulted {
		action()
	}
}

// Report classifies err and returns true when it is structural.
//
// ONLY report errors from SolidPing's own database. Report shuts the process
// down, and the classifier cannot tell whose schema is missing — only the
// caller knows that. internal/checkers/checkpostgres runs lib/pq against
// CUSTOMER databases, where a 42P01 is an ordinary failed check; routing one
// here would take our server down because their table is gone. For a probed
// database, use IsStructural, which has no side effects.
//
// A structural error is latched: the first one logs exactly one line naming the
// fault and triggers the terminal action; later ones are silent, because the
// process is already on its way out and a second flood helps nobody. A nil or
// transient error returns false and changes nothing at all — the caller keeps
// its existing retry and backoff behavior. A nil latch always returns false.
//
// attrs are appended to the log line (component, runner id, …).
func (l *Latch) Report(ctx context.Context, err error, attrs ...any) bool {
	if l == nil {
		// No terminal action is configured, so nothing is terminal: the caller
		// keeps retrying exactly as it did before the latch existed.
		return false
	}

	fault := Describe(err)
	if fault == nil {
		return false
	}

	l.mu.Lock()

	first := l.fault == nil
	if first {
		l.fault = fault
	}

	action := l.action
	l.mu.Unlock()

	if !first {
		return true
	}

	l.logger.ErrorContext(ctx, LogMessage,
		append([]any{
			"reason", fault.Reason,
			"backend", fault.Backend,
			"code", fault.Code,
			"error", fault.err,
		}, attrs...)...)

	if action != nil {
		action()
	}

	return true
}

// Fault returns the latched fault, or nil while the process is healthy.
func (l *Latch) Fault() *StructuralError {
	if l == nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	return l.fault
}

// Err returns the latched fault as an error, or nil. Separate from Fault so
// callers can `return latch.Err()` without the typed-nil trap.
func (l *Latch) Err() error {
	if fault := l.Fault(); fault != nil {
		return fault
	}

	return nil
}
