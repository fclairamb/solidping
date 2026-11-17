package agents

import (
	"sync"
	"time"
)

// DefaultDeprecationLogInterval is how often a deprecation warning is
// repeated for the same agent. A legacy agent polls several times a minute;
// logging every request drowned every other server WARN.
const DefaultDeprecationLogInterval = time.Hour

// DeprecationThrottle lets one deprecation warning through per key (agent
// uid) per interval and counts the ones it swallowed in between. Safe for
// concurrent use. Memory is bounded by the number of distinct agents seen
// within two intervals: stale keys are swept whenever a warning goes through.
type DeprecationThrottle struct {
	interval time.Duration
	now      func() time.Time

	mu      sync.Mutex
	entries map[string]*deprecationEntry
}

type deprecationEntry struct {
	lastLogged time.Time
	suppressed int
}

// NewDeprecationThrottle builds a throttle repeating each key's warning at
// most once per interval.
func NewDeprecationThrottle(interval time.Duration) *DeprecationThrottle {
	return &DeprecationThrottle{
		interval: interval,
		now:      time.Now,
		entries:  make(map[string]*deprecationEntry),
	}
}

// Allow reports whether the warning for key should be logged now and, when
// it should, how many occurrences were suppressed since the last one logged.
// A nil throttle lets everything through.
func (t *DeprecationThrottle) Allow(key string) (bool, int) {
	if t == nil {
		return true, 0
	}

	now := t.now()

	t.mu.Lock()
	defer t.mu.Unlock()

	entry, ok := t.entries[key]
	if ok && now.Sub(entry.lastLogged) < t.interval {
		entry.suppressed++

		return false, 0
	}

	suppressed := 0
	if ok {
		suppressed = entry.suppressed
	}

	t.sweep(now)
	t.entries[key] = &deprecationEntry{lastLogged: now}

	return true, suppressed
}

// sweep drops keys not logged for two intervals (agents gone or upgraded).
// Called with mu held.
func (t *DeprecationThrottle) sweep(now time.Time) {
	for key, entry := range t.entries {
		if now.Sub(entry.lastLogged) >= 2*t.interval {
			delete(t.entries, key)
		}
	}
}
