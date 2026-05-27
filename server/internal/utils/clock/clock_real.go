package clock

import "time"

// Real is the production Clock implementation that delegates to the standard library.
type Real struct{}

// Now returns the current wall-clock time.
func (Real) Now() time.Time { return time.Now() }

// Since returns the elapsed time since t.
func (Real) Since(t time.Time) time.Duration { return time.Since(t) }

// After returns a channel that fires after duration d.
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Sleep pauses the current goroutine for duration d.
func (Real) Sleep(d time.Duration) { time.Sleep(d) }
