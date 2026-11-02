// Package analyticstest provides a recording analytics client for tests.
package analyticstest

import (
	"context"
	"sync"
	"testing"

	"github.com/fclairamb/solidping/server/internal/analytics"
)

// Recorder is an analytics.Client that records what it is given.
type Recorder struct {
	mu         sync.Mutex
	exceptions []analytics.Exception
}

// Install makes a Recorder the process-wide client for the test. Tests using
// it must not be parallel: the default client is process-wide.
func Install(t *testing.T) *Recorder {
	t.Helper()

	rec := &Recorder{}

	analytics.SetDefault(rec)
	t.Cleanup(func() { analytics.SetDefault(nil) })

	return rec
}

// Capture implements analytics.Client.
func (*Recorder) Capture(context.Context, analytics.Event) {}

// CaptureException implements analytics.Client.
func (r *Recorder) CaptureException(_ context.Context, exc *analytics.Exception) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.exceptions = append(r.exceptions, *exc)
}

// Enabled implements analytics.Client.
func (*Recorder) Enabled() bool { return true }

// Close implements analytics.Client.
func (*Recorder) Close(context.Context) error { return nil }

// Exceptions returns the recorded exceptions.
func (r *Recorder) Exceptions() []analytics.Exception {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]analytics.Exception(nil), r.exceptions...)
}
