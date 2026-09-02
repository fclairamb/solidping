package heartbeatpush

import (
	"net"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// sourceIdleTTL is how long an idle per-source bucket survives before the
// cleanup pass evicts it — the same five minutes middleware.RateLimiter uses.
const sourceIdleTTL = 5 * time.Minute

// sourceLimiter is a per-source-IP token bucket shared by both listeners.
//
// It mirrors the budget logic of internal/middleware/ratelimit.go (a
// golang.org/x/time/rate limiter per bucket, idle-evicted) with two deliberate
// differences: there is no waiting room (a beat is fire-and-forget — delaying
// it is worse than dropping it), and the map is hard-capped, because a UDP
// source address is spoofable and an attacker can otherwise mint an unbounded
// number of buckets from a single host.
//
// The bucket key is the IP without the port, so a device that reconnects on a
// new ephemeral port keeps its budget.
type sourceLimiter struct {
	mu      sync.Mutex
	buckets map[string]*sourceBucket
	rate    rate.Limit
	burst   int
	maxKeys int
	lastGC  time.Time
}

type sourceBucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// newSourceLimiter builds a limiter. A non-positive perMinute disables rate
// limiting entirely (allow returns true for everything) — only ever right on a
// closed network, and documented as such.
func newSourceLimiter(perMinute, burst, maxKeys int) *sourceLimiter {
	if perMinute <= 0 {
		return &sourceLimiter{}
	}

	if burst <= 0 {
		burst = 1
	}

	if maxKeys <= 0 {
		maxKeys = 1
	}

	return &sourceLimiter{
		buckets: make(map[string]*sourceBucket),
		rate:    rate.Limit(float64(perMinute) / 60.0),
		burst:   burst,
		maxKeys: maxKeys,
		lastGC:  time.Now(),
	}
}

// allow reports whether a beat from addr may be processed.
func (l *sourceLimiter) allow(addr net.Addr) bool {
	if l == nil || l.buckets == nil {
		return true
	}

	key := sourceKey(addr)

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	l.gcLocked(now)

	bucket, ok := l.buckets[key]
	if !ok {
		// Cap reached and this is a new source: refuse rather than grow. The
		// map is a memory bound, so admitting one more key would defeat it —
		// and a flood large enough to fill it is exactly the case the bound
		// exists for.
		if len(l.buckets) >= l.maxKeys {
			return false
		}

		bucket = &sourceBucket{limiter: rate.NewLimiter(l.rate, l.burst)}
		l.buckets[key] = bucket
	}

	bucket.lastSeen = now

	return bucket.limiter.Allow()
}

// gcLocked evicts idle buckets, at most once a minute, and unconditionally
// once the map is full (so a burst of fresh sources cannot be locked out by
// stale entries that merely have not aged past the sweep interval).
func (l *sourceLimiter) gcLocked(now time.Time) {
	if now.Sub(l.lastGC) < time.Minute && len(l.buckets) < l.maxKeys {
		return
	}

	l.lastGC = now

	for key, bucket := range l.buckets {
		if now.Sub(bucket.lastSeen) > sourceIdleTTL {
			delete(l.buckets, key)
		}
	}
}

// sourceKey reduces an address to its IP, so a device reconnecting on a new
// ephemeral port keeps its budget.
func sourceKey(addr net.Addr) string {
	if addr == nil {
		return "unknown"
	}

	switch typed := addr.(type) {
	case *net.UDPAddr:
		if typed.IP != nil {
			return typed.IP.String()
		}
	case *net.TCPAddr:
		if typed.IP != nil {
			return typed.IP.String()
		}
	}

	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}

	return host
}
