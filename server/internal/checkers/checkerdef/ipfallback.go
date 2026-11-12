package checkerdef

import (
	"context"
	"errors"
	"net"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"
)

const (
	// OutputKeyIPFallback is the result-output key reporting that a dual-stack
	// probe only got through on its fallback address family: {"failed":
	// "ipv6", "reason": "...", "after_ms": N}. It is stamped on a failing
	// probe too, but only an otherwise-up probe is turned into a warning.
	OutputKeyIPFallback = "ip_fallback"

	// OutputKeyMessage is the human-readable one-liner of a result.
	OutputKeyMessage = "message"

	// FamilyFallbackThreshold is how long the first address family may stay
	// pending, when the other family connects, before the probe counts as having
	// fallen back. The Happy Eyeballs race starts the other family after 300 ms,
	// which on its own would flag any merely slow IPv6 path; 1 s is the first SYN
	// retransmit, i.e. the signature of a path that drops packets.
	FamilyFallbackThreshold = time.Second

	fallbackReasonTimeout = "timeout"
)

// FamilyFallback describes a probe whose preferred address family failed or hung
// while the other family answered.
type FamilyFallback struct {
	// Failed is the family that did not answer.
	Failed IPVersion
	// Reason is "timeout" or the dial error text.
	Reason string
	// After is how long the failed family was tried before the other answered
	// (or, for an error, before it failed).
	After time.Duration
}

type familyAttempt struct {
	start   time.Time
	failed  error
	end     time.Time
	pending bool
}

// FamilyFallbackObserver is a pure httptrace observer detecting that an
// auto-family dial fell back from its first family to the other one. It never
// influences dialing. Works for the egress guard's Happy Eyeballs dialer and the
// stdlib dialer alike, as both report each attempt through ConnectStart and
// ConnectDone.
type FamilyFallbackObserver struct {
	mu        sync.Mutex
	now       func() time.Time
	threshold time.Duration
	first     IPVersion
	attempts  map[IPVersion]*familyAttempt
	fallback  *FamilyFallback
}

func (o *FamilyFallbackObserver) clock() time.Time {
	if o.now != nil {
		return o.now()
	}

	return time.Now()
}

func familyOfAddr(addr string) IPVersion {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return IPVersionAuto
	}

	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return IPVersionAuto
	}

	return IPVersionOf(ip)
}

// ClientTrace returns the hooks to attach to the request context.
func (o *FamilyFallbackObserver) ClientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		ConnectStart: func(_, addr string) {
			family := familyOfAddr(addr)
			if family != IPVersionIPv4 && family != IPVersionIPv6 {
				return
			}

			o.mu.Lock()
			defer o.mu.Unlock()

			if o.attempts == nil {
				o.attempts = make(map[IPVersion]*familyAttempt)
			}

			if o.first == "" {
				o.first = family
			}

			if attempt, ok := o.attempts[family]; ok {
				attempt.pending = true

				return
			}

			o.attempts[family] = &familyAttempt{start: o.clock(), pending: true}
		},
		ConnectDone: func(_, addr string, err error) {
			family := familyOfAddr(addr)

			o.mu.Lock()
			defer o.mu.Unlock()

			attempt := o.attempts[family]
			if attempt == nil {
				return
			}

			attempt.pending = false
			attempt.end = o.clock()

			if err != nil && !errors.Is(err, context.Canceled) {
				attempt.failed = err
			}
		},
		GotConn: func(info httptrace.GotConnInfo) {
			if info.Conn == nil {
				return
			}

			remote, ok := info.Conn.RemoteAddr().(*net.TCPAddr)
			if !ok || remote.IP == nil {
				return
			}

			o.evaluate(IPVersionOf(remote.IP))
		},
	}
}

// evaluate runs when a connection is established over winner, then forgets the
// attempts so a redirect hop is judged on its own dial.
func (o *FamilyFallbackObserver) evaluate(winner IPVersion) {
	o.mu.Lock()
	defer o.mu.Unlock()

	first, attempts := o.first, o.attempts
	o.first, o.attempts = "", nil

	if first == "" || first == winner || o.fallback != nil {
		return
	}

	lost, won := attempts[first], attempts[winner]
	if lost == nil || won == nil {
		return
	}

	threshold := o.threshold
	if threshold == 0 {
		threshold = FamilyFallbackThreshold
	}

	if lost.failed != nil {
		reason := lost.failed.Error()

		var netErr net.Error
		if errors.As(lost.failed, &netErr) && netErr.Timeout() {
			reason = fallbackReasonTimeout
		}

		o.fallback = &FamilyFallback{Failed: first, Reason: reason, After: lost.end.Sub(lost.start)}

		return
	}

	// Still pending (or canceled by the winner): a fallback only if it stayed
	// silent long enough to be a lost-packets path, not just a slower one.
	if elapsed := won.end.Sub(lost.start); elapsed >= threshold {
		o.fallback = &FamilyFallback{Failed: first, Reason: fallbackReasonTimeout, After: elapsed}
	}
}

// Fallback returns the detected fallback, or nil.
func (o *FamilyFallbackObserver) Fallback() *FamilyFallback {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.fallback
}

// Apply stamps the fallback onto result: always the ip_fallback output (a
// failure is never upgraded), and, only for an otherwise-up result, the warning
// status and its message. A nil fallback or result is a no-op.
func (f *FamilyFallback) Apply(result *Result) {
	if f == nil || result == nil {
		return
	}

	if result.Output == nil {
		result.Output = make(map[string]any)
	}

	result.Output[OutputKeyIPFallback] = map[string]any{
		"failed":   f.Failed.String(),
		"reason":   f.Reason,
		"after_ms": f.After.Milliseconds(),
	}

	if result.Status != StatusUp {
		return
	}

	result.Status = StatusWarning

	if _, ok := result.Output[OutputKeyMessage]; !ok {
		result.Output[OutputKeyMessage] = f.Failed.Label() + " unreachable from this region, answered over " +
			other(f.Failed).Label()
	}
}

func other(v IPVersion) IPVersion {
	if v == IPVersionIPv6 {
		return IPVersionIPv4
	}

	return IPVersionIPv6
}
