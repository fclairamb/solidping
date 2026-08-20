// Package errorreporttest provides a capturing Sentry transport for tests.
//
// Tests that care about error reporting assert on the *real* *sentry.Event the
// real client produces — scope application, BeforeSend, the lot — with only the
// network hop replaced. Mocking our own wrappers instead would happily keep
// passing while Sentry received nothing, which is exactly the failure mode
// spec 2026-08-20-10 was written about.
//
// NewHub returns a hub of its own rather than calling sentry.Init, so each test
// owns its client and its events and can still run with t.Parallel(). Bind the
// hub onto the context under test (sentry.SetHubOnContext) — every reporting
// path in this codebase looks there first.
package errorreporttest

import (
	"context"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
)

// TestDSN is a syntactically valid DSN pointing nowhere. A DSN is required for
// the client to build its event pipeline; the capturing transport means nothing
// is ever sent.
const TestDSN = "https://public@example.invalid/1"

// Transport is a sentry.Transport that records events instead of sending them.
type Transport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

// Configure implements sentry.Transport.
func (t *Transport) Configure(sentry.ClientOptions) {}

// SendEvent implements sentry.Transport by recording the event.
func (t *Transport) SendEvent(event *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.events = append(t.events, event)
}

// Flush implements sentry.Transport. Nothing is buffered, so it always succeeds.
func (t *Transport) Flush(time.Duration) bool { return true }

// FlushWithContext implements sentry.Transport.
func (t *Transport) FlushWithContext(context.Context) bool { return true }

// Close implements sentry.Transport.
func (t *Transport) Close() {}

// Events returns a snapshot of the events captured so far.
func (t *Transport) Events() []*sentry.Event {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make([]*sentry.Event, len(t.events))
	copy(out, t.events)

	return out
}

// Len returns how many events have been captured.
func (t *Transport) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	return len(t.events)
}

// NewClient builds a real Sentry client whose events land in the returned
// Transport.
func NewClient() (*sentry.Client, *Transport, error) {
	transport := &Transport{}

	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:       TestDSN,
		Transport: transport,
		// Error capture through a custom transport is synchronous: SendEvent
		// has run by the time the call that triggered it returns, so no flush
		// or eventual-consistency dance is needed in tests.
		EnableTracing: false,
		// The log and metric batchers each start a background goroutine that
		// only stops on client.Close(). Nothing here emits logs or metrics to
		// Sentry, and the packages under test run goleak, so keep them off.
		DisableLogs:    true,
		DisableMetrics: true,
	})
	if err != nil {
		return nil, nil, err
	}

	return client, transport, nil
}

// NewHub builds a real Sentry client bound to a fresh hub, with a capturing
// transport. Put the hub on the context under test.
func NewHub() (*sentry.Hub, *Transport, error) {
	client, transport, err := NewClient()
	if err != nil {
		return nil, nil, err
	}

	return sentry.NewHub(client, sentry.NewScope()), transport, nil
}
