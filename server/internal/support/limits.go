package support

import (
	"sync"
	"time"
)

// Abuse ceilings. These endpoints are fed by publicly reachable phone numbers,
// so the tables are attacker-influenced and every one of these is a hard bound
// rather than a nicety.
const (
	// DefaultMessagesPerThreadPerHour caps how many messages one conversation
	// may add in an hour.
	DefaultMessagesPerThreadPerHour = 120
	// DefaultThreadsPerIdentityPerDay caps how many times one identity may open
	// a brand-new thread in a day. Closing and re-opening is legitimate; doing
	// it fifty times is not.
	DefaultThreadsPerIdentityPerDay = 20
	// DefaultMirrorFoldWindow is how long one thread's mirror notifications are
	// folded into the next mail. Someone texting a hundred times must not
	// produce a hundred emails.
	DefaultMirrorFoldWindow = 10 * time.Minute
	// DefaultMirrorsPerHour caps mirrors instance-wide.
	DefaultMirrorsPerHour = 60
)

// counterBucket is one fixed-window counter.
type counterBucket struct {
	windowStart time.Time
	count       int
}

// windowCounter is a fixed-window rate limiter keyed by an arbitrary string.
//
// Deliberately in process memory, and deliberately fixed-window rather than a
// token bucket: this is a ceiling on abuse, not a fairness scheduler. The state
// is worthless after a restart, and a multi-replica deployment enforces the
// ceiling per replica — still a hard bound, just N times the configured one.
// Persisting it would buy nothing and cost a write per inbound message. Same
// reasoning, and the same shape, as audit.FailedLoginFolder.
type windowCounter struct {
	mu      sync.Mutex
	window  time.Duration
	limit   int
	buckets map[string]*counterBucket
}

func newWindowCounter(window time.Duration, limit int) *windowCounter {
	return &windowCounter{
		window:  window,
		limit:   limit,
		buckets: make(map[string]*counterBucket),
	}
}

// allow consumes one unit for key and reports whether it was under the ceiling.
// A non-positive limit disables the ceiling entirely.
func (c *windowCounter) allow(key string, now time.Time) bool {
	if c.limit <= 0 {
		return true
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.evictLocked(now)

	bucket, ok := c.buckets[key]
	if !ok || now.Sub(bucket.windowStart) >= c.window {
		bucket = &counterBucket{windowStart: now}
		c.buckets[key] = bucket
	}

	if bucket.count >= c.limit {
		return false
	}

	bucket.count++

	return true
}

// evictLocked drops buckets whose window has fully elapsed. Without it the map
// grows once per distinct identity, forever — and the identities are supplied
// by whoever is texting us.
func (c *windowCounter) evictLocked(now time.Time) {
	for key, bucket := range c.buckets {
		if now.Sub(bucket.windowStart) >= c.window {
			delete(c.buckets, key)
		}
	}
}
