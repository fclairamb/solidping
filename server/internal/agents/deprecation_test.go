package agents

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDeprecationThrottle: the first warning of an agent goes through, the
// next ones inside the interval are counted, not logged, and the next one
// after the interval carries that count. Agents are throttled independently.
func TestDeprecationThrottle(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	throttle := NewDeprecationThrottle(time.Hour)
	throttle.now = func() time.Time { return clock }

	ok, suppressed := throttle.Allow("agent-a")
	r.True(ok)
	r.Zero(suppressed)

	for range 5 {
		clock = clock.Add(time.Minute)
		ok, _ = throttle.Allow("agent-a")
		r.False(ok)
	}

	ok, suppressed = throttle.Allow("agent-b")
	r.True(ok, "another agent has its own budget")
	r.Zero(suppressed)

	clock = clock.Add(time.Hour)
	ok, suppressed = throttle.Allow("agent-a")
	r.True(ok)
	r.Equal(5, suppressed)

	// agent-b, silent for two intervals, has been swept.
	clock = clock.Add(2 * time.Hour)
	_, _ = throttle.Allow("agent-a")
	r.NotContains(throttle.entries, "agent-b")
}

// TestDeprecationThrottleCapsKeys: with more distinct keys than the cap the
// map never grows past it, the refused keys are counted, and that count shows
// on the next warning let through. Before the cap every key a peer chose was
// kept for two intervals and swept on every allowed key, holding the lock.
func TestDeprecationThrottleCapsKeys(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	throttle := NewDeprecationThrottleWith(time.Hour, 8, func() time.Time { return clock })

	allowed := 0

	for i := range 1000 {
		if ok, _ := throttle.Allow(fmt.Sprintf("key-%d", i)); ok {
			allowed++
		}

		r.LessOrEqual(throttle.Len(), 8, "the map must never grow past its cap")
	}

	r.Equal(8, allowed, "only the keys that fit are let through")
	r.Equal(992, throttle.overflow)

	// A known key, once its interval is over, carries the overflow count.
	clock = clock.Add(time.Hour)
	ok, suppressed := throttle.Allow("key-0")
	r.True(ok)
	r.Equal(992, suppressed, "the refused keys must show in the next line")
	r.Zero(throttle.overflow)

	// Once the old keys are stale, the sweep frees room for new ones.
	clock = clock.Add(2 * time.Hour)
	ok, _ = throttle.Allow("key-new")
	r.True(ok)
	r.LessOrEqual(throttle.Len(), 2)
}

// TestDeprecationThrottleSweepsOncePerInterval: the sweep no longer runs on
// every allowed key; a stale key survives until the next interval boundary.
func TestDeprecationThrottleSweepsOncePerInterval(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	throttle := NewDeprecationThrottleWith(time.Hour, 0, func() time.Time { return clock })

	_, _ = throttle.Allow("old") // sweeps at t0
	clock = clock.Add(2*time.Hour + 30*time.Minute)
	_, _ = throttle.Allow("a") // sweeps: "old" is 2h30 old
	r.NotContains(throttle.entries, "old")

	_, _ = throttle.Allow("b")
	clock = clock.Add(2*time.Hour + 10*time.Minute)
	_, _ = throttle.Allow("c") // sweep: a, b gone
	r.NotContains(throttle.entries, "a")
	r.NotContains(throttle.entries, "b")

	clock = clock.Add(10 * time.Minute)
	sweptAt := throttle.lastSweep
	_, _ = throttle.Allow("d")
	r.Equal(sweptAt, throttle.lastSweep, "no sweep inside the same interval")
}
