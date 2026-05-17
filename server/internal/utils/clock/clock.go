// Package clock provides a time abstraction for deterministic testing.
package clock

import "time"

// Clock is a source of time. Real wraps the standard library; Fake allows
// tests to advance time deterministically.
type Clock interface {
	Now() time.Time
	Since(t time.Time) time.Duration
	After(d time.Duration) <-chan time.Time
	Sleep(d time.Duration)
}
