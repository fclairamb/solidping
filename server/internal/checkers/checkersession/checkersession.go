// Package checkersession holds the session-lifecycle plumbing shared by the
// checkers that drive a heavy, stateful session per run: a headless Chrome
// (checkbrowser) and an interactive RDP logon (checkrdp).
//
// Three pieces, factored out of checkbrowser so the two checkers cannot drift:
//
//   - Slots, a process-wide counting semaphore whose wait is bounded by the
//     caller's context (the check's own timeout), so a saturated worker
//     reports a timeout instead of queueing invisibly;
//   - the slot-timeout error shape and its timeout verdict (SlotTimeoutResult);
//   - Classifier, the infrastructure-vs-target error split the JS runtime
//     turns into throw-vs-return.
//
// Each checker keeps its OWN Slots instance: a full Chrome pool says nothing
// about RDP capacity and vice versa. Only the code is shared.
package checkersession

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ErrSlotTimeout is the generic "every slot was busy for as long as the
// caller's context allowed" failure. Each checker's own slot-timeout error
// (NewSlotTimeoutError) matches it under errors.Is, so shared code can
// recognize any of them.
var ErrSlotTimeout = errors.New("timed out waiting for a free session slot")

// slotTimeoutError is one checker's slot-timeout error: its own operator-facing
// sentence, recognized as ErrSlotTimeout.
type slotTimeoutError struct {
	msg string
}

func (e *slotTimeoutError) Error() string { return e.msg }

// Is makes every checker's slot timeout match the shared sentinel.
func (e *slotTimeoutError) Is(target error) bool { return target == ErrSlotTimeout }

// Infra reports false: a slot timeout means the worker is full, not broken.
func (e *slotTimeoutError) Infra() bool { return false }

// NewSlotTimeoutError builds a checker's slot-timeout error. Declare it once
// as a package variable so callers can also compare against it directly.
func NewSlotTimeoutError(msg string) error {
	return &slotTimeoutError{msg: msg}
}

// Slots is a counting semaphore with a context-bounded wait.
type Slots struct {
	ch         chan struct{}
	errTimeout error
}

// NewSlots returns a semaphore of the given capacity. errTimeout is what
// Acquire returns when the caller's context ends before a slot frees up; it
// should come from NewSlotTimeoutError.
func NewSlots(capacity int, errTimeout error) *Slots {
	return &Slots{ch: make(chan struct{}, capacity), errTimeout: errTimeout}
}

// Acquire waits for a free slot, giving up when ctx is done. The wait happens
// INSIDE the caller's budget on purpose. The returned release frees the slot;
// calling it more than once is a no-op.
func (s *Slots) Acquire(ctx context.Context) (func(), error) {
	select {
	case s.ch <- struct{}{}:
		var once sync.Once

		return func() { once.Do(func() { <-s.ch }) }, nil
	case <-ctx.Done():
		return nil, s.errTimeout
	}
}

// InUse reports how many slots are currently held. For tests and metrics.
func (s *Slots) InUse() int { return len(s.ch) }

// Capacity reports the semaphore's size.
func (s *Slots) Capacity() int { return cap(s.ch) }

// SlotTimeoutResult renders a slot timeout as the check's verdict: StatusTimeout
// with the error in the output, never Down (the target was not even tried) and
// never Error (nothing is broken). ok is false when err is not a slot timeout.
func SlotTimeoutResult(
	err error, start time.Time, metrics, output map[string]any,
) (*checkerdef.Result, bool) {
	if !errors.Is(err, ErrSlotTimeout) {
		return nil, false
	}

	output[checkerdef.OutputKeyError] = err.Error()

	return &checkerdef.Result{
		Status:   checkerdef.StatusTimeout,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}, true
}

// Classifier is the infrastructure-vs-target error split. Infrastructure means
// OUR side is gone (the session, the transport, the sidecar) and the JS
// runtime throws it as status `error`; anything else comes back to the script
// as `{ ok: false }` so the script decides whether the target is down.
type Classifier struct {
	// InfraSentinels are errors that always mean infrastructure.
	InfraSentinels []error
	// TargetSentinels are errors that always mean a target-side verdict.
	TargetSentinels []error
	// Fallback decides every error the rules above leave open. Nil means
	// "not infrastructure".
	Fallback func(err error) bool
}

// infraMarker is implemented by error types that know their own class.
type infraMarker interface {
	Infra() bool
}

// Infra classifies err. The order is the contract:
//
//  1. nil is never infrastructure;
//  2. an error type that declares its class (an Infra() bool method) wins;
//  3. InfraSentinels, then a slot timeout (the worker is full, not broken),
//     then TargetSentinels;
//  4. a context deadline or cancel is the check's OWN timeout, never infra;
//  5. Fallback.
func (c Classifier) Infra(err error) bool {
	if err == nil {
		return false
	}

	var marker infraMarker
	if errors.As(err, &marker) {
		return marker.Infra()
	}

	for _, sentinel := range c.InfraSentinels {
		if errors.Is(err, sentinel) {
			return true
		}
	}

	if errors.Is(err, ErrSlotTimeout) {
		return false
	}

	for _, sentinel := range c.TargetSentinels {
		if errors.Is(err, sentinel) {
			return false
		}
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}

	if c.Fallback != nil {
		return c.Fallback(err)
	}

	return false
}
