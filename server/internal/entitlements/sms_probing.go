package entitlements

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Destination-probing detection (spec 2026-11-15-01).
//
// A burst of allow-list refusals from one org is the signature of SMS pumping
// / IRSF probing: an account adding destinations in high-cost countries and
// requesting a code for each, to find one the instance will pay for. One
// refusal is a typo; several within minutes is a probe, and an operator must
// hear about it.
const (
	// smsProbeRefusals is how many allow-list refusals within smsProbeWindow
	// flag an org.
	smsProbeRefusals = 3
	// smsProbeWindow is the sliding window the refusals are counted over.
	smsProbeWindow = 10 * time.Minute
	// smsProbeBlockFor is how long a flagged org stays flagged: at most one
	// alert per org in that time, and its verification sends are refused.
	smsProbeBlockFor = time.Hour
)

// SMSProbingAlert describes one org flagged for destination probing.
type SMSProbingAlert struct {
	// OrgUID is the flagged organization.
	OrgUID string
	// Refusals is how many allow-list refusals triggered the alert.
	Refusals int
	// Window is the window those refusals happened in.
	Window time.Duration
	// Prefixes are the distinct refused destination prefixes (never a full
	// number), sorted.
	Prefixes []string
	// BlockFor is how long the org stays flagged, from the alert.
	BlockFor time.Duration
	// BlockedUntil is when the org's verification sends are allowed again.
	BlockedUntil time.Time
}

// SMSProbingNotifier is told when an org is flagged. It is a callback rather
// than an opsnotify import so this package stays low in the import graph; the
// app wiring turns it into an operator notice.
type SMSProbingNotifier func(ctx context.Context, alert *SMSProbingAlert)

// WithSMSProbingNotifier installs the callback raised when an org reaches the
// allow-list refusal threshold. Nil disables the alert (the block still
// applies).
func WithSMSProbingNotifier(notify SMSProbingNotifier) Option {
	return func(s *Service) {
		s.smsProbe.notify = notify
	}
}

type smsProbeRefusal struct {
	at     time.Time
	prefix string
}

// smsProbeTracker is the per-org sliding window of allow-list refusals plus the
// flag set when an org crosses the threshold. In-memory and process-local,
// like the runaway buckets.
type smsProbeTracker struct {
	mu       sync.Mutex
	refusals map[string][]smsProbeRefusal
	flagged  map[string]time.Time // orgUID -> blocked until
	notify   SMSProbingNotifier
}

// recordRefusal adds an allow-list refusal for orgUID at now and returns the
// alert to raise when this refusal flags the org, nil otherwise. An org that is
// already flagged raises nothing more until the flag lapses.
func (t *smsProbeTracker) recordRefusal(orgUID, prefix string, now time.Time) *SMSProbingAlert {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.refusals == nil {
		t.refusals = make(map[string][]smsProbeRefusal)
		t.flagged = make(map[string]time.Time)
	}

	t.pruneLocked(now)

	t.refusals[orgUID] = append(t.refusals[orgUID], smsProbeRefusal{at: now, prefix: prefix})

	if _, flagged := t.flagged[orgUID]; flagged {
		return nil
	}

	window := t.refusals[orgUID]
	if len(window) < smsProbeRefusals {
		return nil
	}

	until := now.Add(smsProbeBlockFor)
	t.flagged[orgUID] = until

	seen := make(map[string]struct{}, len(window))
	prefixes := make([]string, 0, len(window))

	for i := range window {
		prefix := window[i].prefix
		if _, dup := seen[prefix]; dup {
			continue
		}

		seen[prefix] = struct{}{}
		prefixes = append(prefixes, prefix)
	}

	sort.Strings(prefixes)

	return &SMSProbingAlert{
		OrgUID:       orgUID,
		Refusals:     len(window),
		Window:       smsProbeWindow,
		Prefixes:     prefixes,
		BlockFor:     smsProbeBlockFor,
		BlockedUntil: until,
	}
}

// blocked reports whether orgUID is flagged at now.
func (t *smsProbeTracker) blocked(orgUID string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	until, ok := t.flagged[orgUID]
	if !ok {
		return false
	}

	if !now.Before(until) {
		delete(t.flagged, orgUID)

		return false
	}

	return true
}

// pruneLocked drops refusals older than the window and lapsed flags, for every
// org, so an org that stops probing does not keep its entries forever.
func (t *smsProbeTracker) pruneLocked(now time.Time) {
	cutoff := now.Add(-smsProbeWindow)

	for org, window := range t.refusals {
		kept := window[:0]

		for i := range window {
			if window[i].at.After(cutoff) {
				kept = append(kept, window[i])
			}
		}

		if len(kept) == 0 {
			delete(t.refusals, org)
		} else {
			t.refusals[org] = kept
		}
	}

	for org, until := range t.flagged {
		if !now.Before(until) {
			delete(t.flagged, org)
		}
	}
}

// noteCountryRefusal feeds one allow-list refusal into the probing tracker and
// raises the alert when it flags the org. The notifier runs outside the lock.
func (s *Service) noteCountryRefusal(ctx context.Context, orgUID, prefix string) {
	alert := s.smsProbe.recordRefusal(orgUID, prefix, s.now())
	if alert == nil || s.smsProbe.notify == nil {
		return
	}

	s.smsProbe.notify(ctx, alert)
}

// SMSVerificationBlocked reports whether orgUID is flagged for destination
// probing: for smsProbeBlockFor after the alert, every phone/WhatsApp
// verification send of the org is refused without reaching a provider.
func (s *Service) SMSVerificationBlocked(orgUID string) bool {
	return s.smsProbe.blocked(orgUID, s.now())
}
