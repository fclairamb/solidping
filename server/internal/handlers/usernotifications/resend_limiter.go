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

// destinationLimiter caps how many DISTINCT destinations one organization may
// send verification codes to within verifyDestinationWindow (spec
// 2026-11-15-01). The per-contact resendLimiter alone lets an account probe an
// unbounded number of numbers by creating a new contact for each; this is the
// cross-contact bound. Re-sending to a destination already counted in the
// window does not consume a slot. In-memory and process-local, like
// resendLimiter.
type destinationLimiter struct {
	mu   sync.Mutex
	seen map[string]map[string]time.Time // orgUID -> destination -> first send
}

func newDestinationLimiter() *destinationLimiter {
	return &destinationLimiter{seen: make(map[string]map[string]time.Time)}
}

// allow records destination for orgUID at now and reports whether it fits in
// limit distinct destinations per window. Entries older than the window are
// pruned on each call.
func (l *destinationLimiter) allow(orgUID, destination string, limit int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Other orgs are pruned against the wall clock, never against the caller's
	// now: an injected (or skewed) clock must not evict another org's slots.
	// The caller's own org is pruned against now.
	wallCutoff := time.Now().Add(-verifyDestinationWindow)
	cutoff := now.Add(-verifyDestinationWindow)

	for org, destinations := range l.seen {
		orgCutoff := wallCutoff
		if org == orgUID {
			orgCutoff = cutoff
		}

		for dest, at := range destinations {
			if !at.After(orgCutoff) {
				delete(destinations, dest)
			}
		}

		if len(destinations) == 0 {
			delete(l.seen, org)
		}
	}

	destinations := l.seen[orgUID]
	if _, counted := destinations[destination]; counted {
		return true
	}

	if len(destinations) >= limit {
		return false
	}

	if destinations == nil {
		destinations = make(map[string]time.Time)
		l.seen[orgUID] = destinations
	}

	destinations[destination] = now

	return true
}
