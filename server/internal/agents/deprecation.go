package agents

import (
	"sync"
	"time"
)

// DefaultDeprecationLogInterval is how often a deprecation warning is
// repeated for the same agent. A legacy agent polls several times a minute;
// logging every request drowned every other server WARN.
const DefaultDeprecationLogInterval = time.Hour

// DefaultDeprecationMaxKeys caps how many keys one throttle tracks. Callers key
// on server-chosen values (agent uid, a fixed reason), so a real instance stays
// far below it; the cap only matters if a key ever carries something a peer
// controls.
const DefaultDeprecationMaxKeys = 4096

// DeprecationThrottle lets one warning through per key per interval and counts
// the ones it swallowed in between. Safe for concurrent use. Memory is bounded
// by maxKeys whatever the keys are: past the cap a new key is refused (and
// counted as overflow) until stale keys are swept. Keys not logged for two
// intervals are swept, at most once per interval.
type DeprecationThrottle struct {
	interval time.Duration
	maxKeys  int
	now      func() time.Time

	mu        sync.Mutex
	entries   map[string]*deprecationEntry
	lastSweep time.Time
	// overflow counts warnings refused because the map was full. It is added
	// to the suppressed count of the next warning let through, so a flood
	// still shows in the logs.
	overflow int
}

type deprecationEntry struct {
	lastLogged time.Time
	suppressed int
}

// NewDeprecationThrottle builds a throttle repeating each key's warning at
// most once per interval, tracking at most DefaultDeprecationMaxKeys keys.
func NewDeprecationThrottle(interval time.Duration) *DeprecationThrottle {
	return NewDeprecationThrottleWith(interval, DefaultDeprecationMaxKeys, nil)
}

// NewDeprecationThrottleWith is NewDeprecationThrottle with an explicit key
// cap (<= 0: DefaultDeprecationMaxKeys) and clock (nil: time.Now).
func NewDeprecationThrottleWith(interval time.Duration, maxKeys int, now func() time.Time) *DeprecationThrottle {
	if maxKeys <= 0 {
		maxKeys = DefaultDeprecationMaxKeys
	}

	if now == nil {
		now = time.Now
	}

	return &DeprecationThrottle{
		interval: interval,
		maxKeys:  maxKeys,
		now:      now,
		entries:  make(map[string]*deprecationEntry),
	}
}

// Allow reports whether the warning for key should be logged now and, when
// it should, how many occurrences were suppressed since the last one logged
// (plus any refused while the throttle was full). A nil throttle lets
// everything through.
func (t *DeprecationThrottle) Allow(key string) (bool, int) {
	if t == nil {
		return true, 0
	}

	now := t.now()

	t.mu.Lock()
	defer t.mu.Unlock()

	if now.Sub(t.lastSweep) >= t.interval {
		t.sweep(now)
	}

	entry, ok := t.entries[key]
	if ok && now.Sub(entry.lastLogged) < t.interval {
		entry.suppressed++

		return false, 0
	}

	if !ok && len(t.entries) >= t.maxKeys {
		t.overflow++

		return false, 0
	}

	suppressed := t.overflow
	t.overflow = 0

	if ok {
		suppressed += entry.suppressed
		entry.lastLogged = now
		entry.suppressed = 0
	} else {
		t.entries[key] = &deprecationEntry{lastLogged: now}
	}

	return true, suppressed
}

// Len returns how many keys the throttle currently tracks.
func (t *DeprecationThrottle) Len() int {
	if t == nil {
		return 0
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	return len(t.entries)
}

// sweep drops keys not logged for two intervals (agents gone or upgraded).
// Called with mu held.
func (t *DeprecationThrottle) sweep(now time.Time) {
	t.lastSweep = now

	for key, entry := range t.entries {
		if now.Sub(entry.lastLogged) >= 2*t.interval {
			delete(t.entries, key)
		}
	}
}
