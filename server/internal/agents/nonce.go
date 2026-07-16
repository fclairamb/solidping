package agents

import (
	"sync"
	"time"
)

// NonceCache remembers recently-seen (agent, nonce) pairs so a reconnect
// signature cannot be replayed within the clock-skew window. Entries older than
// the retention window are pruned lazily on insert. Process-local: it defends a
// single instance; a replay against a different replica within the window is out
// of scope for phase 1 (the signature still binds method|path|timestamp, and the
// timestamp is skew-bounded).
type NonceCache struct {
	mu       sync.Mutex
	seen     map[string]time.Time
	retain   time.Duration
	nowFn    func() time.Time
	gcEvery  int
	inserts  int
	maxEntry int
}

// NewNonceCache creates a nonce cache that retains entries for `retain`
// (typically 2x the clock skew so a replay is impossible across the whole window).
func NewNonceCache(retain time.Duration) *NonceCache {
	return &NonceCache{
		seen:     make(map[string]time.Time),
		retain:   retain,
		nowFn:    time.Now,
		gcEvery:  256,
		maxEntry: 100000,
	}
}

// CheckAndStore records (agentUID, nonce). It returns ErrReplayedNonce if the
// pair was already seen within the retention window, otherwise nil.
func (c *NonceCache) CheckAndStore(agentUID, nonce string) error {
	key := agentUID + "\x00" + nonce
	now := c.nowFn()

	c.mu.Lock()
	defer c.mu.Unlock()

	c.maybeGC(now)

	if ts, ok := c.seen[key]; ok && now.Sub(ts) <= c.retain {
		return ErrReplayedNonce
	}

	c.seen[key] = now
	c.inserts++

	return nil
}

// maybeGC prunes expired entries periodically (every gcEvery inserts) to bound
// memory. Also prunes when the map exceeds the hard cap.
func (c *NonceCache) maybeGC(now time.Time) {
	if c.inserts%c.gcEvery != 0 && len(c.seen) < c.maxEntry {
		return
	}

	for k, ts := range c.seen {
		if now.Sub(ts) > c.retain {
			delete(c.seen, k)
		}
	}
}

// Len reports the current number of cached nonces (for tests/metrics).
func (c *NonceCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.seen)
}
