package usernotifications

import (
	"sync"
	"time"
)

// resendLimiter throttles verification-code issuance per contact: at most
// verifyResendMax issuances within any verifyResendWindow. In-memory and
// process-local — a good-enough anti-abuse control layered on top of the
// durable per-code TTL and attempt cap.
type resendLimiter struct {
	mu    sync.Mutex
	sends map[string][]time.Time
}

func newResendLimiter() *resendLimiter {
	return &resendLimiter{sends: make(map[string][]time.Time)}
}

// allow records an issuance at now for key and reports whether it is within
// the rate limit. Timestamps older than the window are pruned on each call.
func (l *resendLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := now.Add(-verifyResendWindow)
	kept := make([]time.Time, 0, len(l.sends[key]))
	for _, t := range l.sends[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}

	if len(kept) >= verifyResendMax {
		l.sends[key] = kept

		return false
	}

	l.sends[key] = append(kept, now)

	return true
}
