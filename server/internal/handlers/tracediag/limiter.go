package tracediag

import (
	"sync"
	"time"
)

// orgLimiter is the per-organization ceiling on traces started per minute.
//
// WHY THIS EXISTS AT ALL: when one upstream fails, every check behind it opens
// an incident inside the same minute. Without a ceiling that is one 15-second
// sweep per check — hundreds of concurrent traces, all describing the same
// broken hop, all competing for the same egress the monitoring itself needs.
// The limit is the difference between "diagnostics" and "a second outage".
//
// A FIXED WINDOW, not a token bucket, and in-process rather than in the
// database. Both are deliberate: the quantity being bounded is a burst inside
// one minute (exactly what a fixed window expresses), and a limiter that had to
// round-trip to the database on the incident path would put a query in front of
// a feature whose whole contract is that it costs the incident nothing. On a
// multi-replica deployment each replica therefore carries its own budget, which
// over-admits by a factor of the replica count — acceptable for a burst guard,
// and stated here so nobody reads the number as an absolute.
type orgLimiter struct {
	mu sync.Mutex
	// limit is traces per window per org. 0 disables limiting entirely.
	limit   int
	buckets map[string]*minuteBucket
}

// minuteBucket counts one org's traces inside one wall-clock minute.
type minuteBucket struct {
	minuteStart time.Time
	count       int
}

// limiterSweepThreshold is how many org buckets may accumulate before a stale
// sweep runs. Buckets are tiny, and an instance with thousands of orgs opening
// incidents in the same minute has bigger problems, so this only exists so the
// map cannot grow without bound over a process's lifetime.
const limiterSweepThreshold = 1024

func newOrgLimiter(limit int) *orgLimiter {
	return &orgLimiter{limit: limit, buckets: map[string]*minuteBucket{}}
}

// allow charges one trace against the org's budget for the current minute.
func (l *orgLimiter) allow(orgUID string, now time.Time) bool {
	if l.limit <= 0 {
		return true
	}

	minute := now.Truncate(time.Minute)

	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.buckets) >= limiterSweepThreshold {
		l.sweepLocked(minute)
	}

	bucket, ok := l.buckets[orgUID]
	if !ok || !bucket.minuteStart.Equal(minute) {
		l.buckets[orgUID] = &minuteBucket{minuteStart: minute, count: 1}

		return true
	}

	if bucket.count >= l.limit {
		return false
	}

	bucket.count++

	return true
}

// sweepLocked drops buckets from earlier minutes. Caller holds the lock.
func (l *orgLimiter) sweepLocked(minute time.Time) {
	for org, bucket := range l.buckets {
		if !bucket.minuteStart.Equal(minute) {
			delete(l.buckets, org)
		}
	}
}
