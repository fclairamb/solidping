package agents

import (
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
