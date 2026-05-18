package clock

import (
	"time"

	"github.com/jonboulle/clockwork"
)

// Fake is a test Clock implementation that wraps clockwork.FakeClock.
// Use NewFake to create an instance, and Advance to move time forward.
// Goroutines blocked on After or Sleep will be woken when their deadline
// is passed by Advance.
type Fake struct {
	fake *clockwork.FakeClock
}

// NewFake creates a new Fake clock starting at start.
func NewFake(start time.Time) *Fake {
	return &Fake{fake: clockwork.NewFakeClockAt(start)}
}

// Advance moves the fake clock forward by d, waking any goroutines
// blocked on After or Sleep whose deadline has been passed.
func (f *Fake) Advance(d time.Duration) {
	f.fake.Advance(d)
}

// Now returns the current fake time.
func (f *Fake) Now() time.Time {
	return f.fake.Now()
}

// Since returns the elapsed fake time since t.
func (f *Fake) Since(t time.Time) time.Duration {
	return f.fake.Now().Sub(t)
}

// After returns a channel that fires after d of fake time has elapsed.
func (f *Fake) After(d time.Duration) <-chan time.Time {
	return f.fake.After(d)
}

// Sleep blocks until d of fake time has elapsed via Advance calls.
func (f *Fake) Sleep(d time.Duration) {
	f.fake.Sleep(d)
}
