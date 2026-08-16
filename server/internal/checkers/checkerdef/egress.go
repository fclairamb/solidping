package checkerdef

import (
	"net"
	"sync"
	"time"
)

// egressProbeTargets are the addresses the self-probe routes towards, one per
// family. Both are reserved documentation prefixes (RFC 5737 TEST-NET-1 and RFC
// 3849) that are never assigned to a real host — which is safe precisely
// because connect(2) on a datagram socket sends no packet and only performs a
// route lookup, so their reachability is irrelevant. What matters is that they
// are OFF-LINK: the lookup then has to consult the host's default route for
// that family instead of matching loopback or a link-local shortcut, which is
// exactly the property "can this worker originate IPv6 at all" depends on.
var egressProbeTargets = map[IPVersion]net.IP{ //nolint:gochecknoglobals // constant lookup table of probe targets
	IPVersionIPv4: net.ParseIP("192.0.2.1"),
	IPVersionIPv6: net.ParseIP("2001:db8::1"),
}

// ProbeEgress reports, per address family, whether this host can originate
// traffic at all. It is the proactive half of the ErrWorkerNoEgress story: the
// same question the per-run pre-flight answers, asked before a check is ever
// created rather than after one has already failed.
//
// It is deliberately built on DialEgressProbe so the codebase holds exactly ONE
// route-lookup implementation — a second, subtly different one would be free to
// disagree with the authority, which is the failure this whole feature exists
// to avoid.
//
// The result is a HINT, advertised so a user can pick a region that works. It
// must never gate execution: SelectIPAddrWithProbe's per-run probe remains the
// only authority, so a host that gains v6 runs immediately (no stale flag
// blocks it) and a host that loses v6 still fails with ErrWorkerNoEgress rather
// than a false DOWN.
func ProbeEgress() map[IPVersion]bool {
	return ProbeEgressWith(DialEgressProbe)
}

// ProbeEgressWith is ProbeEgress with the route lookup injectable, the same
// seam SelectIPAddrWithProbe exposes: tests drive the v4-only and dual-stack
// hosts deterministically instead of depending on the CI runner's networking.
func ProbeEgressWith(probe EgressProbe) map[IPVersion]bool {
	out := make(map[IPVersion]bool, len(egressProbeTargets))

	for version, ip := range egressProbeTargets {
		out[version] = probe(version, ip) == nil
	}

	return out
}

// EgressCache memoizes a ProbeEgress result for a short TTL. A worker reports
// its egress on every heartbeat and (for a deported agent) on every claim
// frame, and a route lookup per claim would be wasteful churn for an answer
// that changes approximately never. The TTL is what keeps "probed at report
// time, not at process start" true: a host that gains or loses v6 still
// converges within one TTL plus one heartbeat, with no restart.
type EgressCache struct {
	ttl   time.Duration
	probe func() map[IPVersion]bool

	mu   sync.Mutex
	at   time.Time
	last map[IPVersion]bool
}

// NewEgressCache builds a cache over ProbeEgress with the given TTL. A
// non-positive TTL probes on every call.
func NewEgressCache(ttl time.Duration) *EgressCache {
	return NewEgressCacheWith(ttl, ProbeEgress)
}

// NewEgressCacheWith is NewEgressCache with the prober injectable (tests).
func NewEgressCacheWith(ttl time.Duration, probe func() map[IPVersion]bool) *EgressCache {
	return &EgressCache{ttl: ttl, probe: probe}
}

// Get returns the cached families, re-probing when the entry has expired.
func (c *EgressCache) Get() map[IPVersion]bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if c.last != nil && c.ttl > 0 && now.Sub(c.at) < c.ttl {
		return c.last
	}

	c.last = c.probe()
	c.at = now

	return c.last
}
