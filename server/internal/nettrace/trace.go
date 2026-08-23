// Package nettrace is the MTR-style path prober SolidPing attaches to an
// incident opened by a network-reachability failure (spec 2026-08-21-10).
//
// It is split deliberately into two halves:
//
//   - THIS FILE and the types beside it are the round/aggregation logic, which
//     knows nothing about sockets. It talks to a Prober interface, so every
//     behavior that matters (loss math, RTT aggregation, stopping at the
//     target, budget exhaustion, silent-hop cutoff) is unit-testable with a
//     fake and without a single privileged syscall.
//   - prober_icmp.go / prober_tcp.go / detect.go are the platform half, where
//     raw sockets, capabilities and their absence live.
//
// That split is what makes the spec's "flaky-by-privilege is not acceptable"
// requirement achievable: the logic is tested everywhere, and only the socket
// layer is gated on a deterministic capability probe.
package nettrace

import (
	"context"
	"errors"
	"net"
	"sort"
	"time"
)

// Mode names how the trace was probed. It is recorded in every capture because
// the three modes see genuinely different things, and a hop list read without
// knowing which one produced it is a hop list read wrong.
type Mode string

const (
	// ModeICMPRaw is a raw ICMP echo socket (`ip4:icmp` / `ip6:ipv6-icmp`).
	// Needs root or CAP_NET_RAW. Sees every hop that answers TTL-exceeded.
	ModeICMPRaw Mode = "icmp"
	// ModeICMPUDP is the unprivileged ICMP DATAGRAM socket (`udp4`/`udp6`):
	// default on macOS, and on Linux available to the gids inside
	// net.ipv4.ping_group_range. Sees the same hops as ModeICMPRaw.
	//
	// For readers of the spec: this is what it calls the
	// "unprivileged UDP" tier. Classic UDP-to-high-ports traceroute is NOT an
	// unprivileged mode — it still needs a raw socket to READ the ICMP replies
	// — so the datagram ICMP socket is the tier that actually exists.
	ModeICMPUDP Mode = "icmp-udp"
	// ModeTCP is the last resort: TTL-stepped TCP connects to the check's own
	// target port, using only ordinary sockets.
	//
	// IT CANNOT SEE INTERMEDIATE HOP ADDRESSES. A plain connect() surfaces the
	// errno, not the ICMP source address of the router that sent it (that needs
	// IP_RECVERR, which is Linux-only and not portable). What it produces is a
	// REACHABILITY LADDER: at which TTL the target starts answering, and
	// therefore how many hops away it is. Captures in this mode set
	// HopAddressesVisible=false and every surface must say so rather than
	// rendering a column of blanks as if the routers were merely shy.
	ModeTCP Mode = "tcp"
)

// Defaults for a trace. The budget is the spec's hard ~15 s ceiling: the whole
// capture is best-effort evidence attached after the incident already opened,
// so it must never be the reason anything waits.
const (
	DefaultRounds       = 3
	DefaultMaxHops      = 30
	DefaultBudget       = 15 * time.Second
	DefaultProbeTimeout = time.Second
	// DefaultMaxSilentHops stops the sweep after this many consecutive
	// unanswered TTLs. A black-holed path answers nothing past the drop point,
	// and walking the remaining 25 TTLs at a second each would eat the whole
	// budget to learn nothing the last responding hop did not already say.
	DefaultMaxSilentHops = 5
	// DefaultResolveTimeout bounds ONE reverse lookup. Names are a nicety; the
	// addresses are the evidence.
	DefaultResolveTimeout = 300 * time.Millisecond
)

// ErrNoTarget means the caller supplied no address to trace to.
var ErrNoTarget = errors.New("nettrace: no target address")

// Reply is one prober answer.
type Reply struct {
	// From is the hop that answered, or nil when the prober cannot observe it
	// (ModeTCP) — which is NOT the same as "nothing answered". Nothing
	// answering is reported as ErrProbeTimeout from Probe.
	From net.IP
	// RTT is the round trip of this probe.
	RTT time.Duration
	// Final reports that the TARGET itself answered, so the sweep can stop
	// rather than walking to MaxHops.
	Final bool
	// Unreachable reports an explicit destination-unreachable rather than a
	// TTL-exceeded. It also terminates the sweep: a router has made a routing
	// decision and nothing past it will answer.
	Unreachable bool
}

// ErrProbeTimeout is what a Prober returns when a probe went unanswered. It is
// an ordinary outcome — most traces contain some — and is recorded as loss, not
// as a failure of the trace.
var ErrProbeTimeout = errors.New("nettrace: probe timed out")

// Prober sends one TTL-limited probe and waits for one reply. Implementations
// are NOT required to be safe for concurrent use; Trace calls them serially.
type Prober interface {
	// Mode reports which of the three probe styles this prober implements.
	Mode() Mode
	// Probe sends a probe to dst with the given TTL and sequence number.
	// It returns ErrProbeTimeout when nothing came back before ctx expired.
	Probe(ctx context.Context, dst net.IP, ttl, seq int) (Reply, error)
	// Close releases the socket. Safe to call more than once.
	Close() error
}

// Resolver is the reverse-DNS seam, so tests never touch the network.
type Resolver interface {
	LookupAddr(ctx context.Context, addr string) ([]string, error)
}

// Options configures one trace.
type Options struct {
	// Host is the configured hostname, recorded for display only.
	Host string
	// Address is the IP to trace to — the one the failing probe actually
	// dialed, so the trace follows the same family and the same machine.
	Address net.IP
	// Port is the target port, used by ModeTCP and recorded either way.
	Port int
	// Rounds is how many times each TTL is probed. <= 0 uses DefaultRounds.
	Rounds int
	// MaxHops caps the TTL walk. <= 0 uses DefaultMaxHops.
	MaxHops int
	// Budget is the hard wall-clock ceiling for the whole capture, reverse DNS
	// included. <= 0 uses DefaultBudget.
	Budget time.Duration
	// ProbeTimeout bounds one probe. <= 0 uses DefaultProbeTimeout.
	ProbeTimeout time.Duration
	// MaxSilentHops stops the sweep after this many consecutive unanswered
	// TTLs. <= 0 uses DefaultMaxSilentHops.
	MaxSilentHops int
	// Resolver, when non-nil, is used for per-hop reverse DNS. Nil disables it.
	Resolver Resolver
	// ResolveTimeout bounds one reverse lookup. <= 0 uses
	// DefaultResolveTimeout.
	ResolveTimeout time.Duration
	// Now is the clock, injectable so budget behavior is testable without
	// sleeping. Nil uses time.Now.
	Now func() time.Time
}

func (o *Options) rounds() int {
	if o.Rounds <= 0 {
		return DefaultRounds
	}

	return o.Rounds
}

func (o *Options) maxHops() int {
	if o.MaxHops <= 0 {
		return DefaultMaxHops
	}

	return o.MaxHops
}

func (o *Options) budget() time.Duration {
	if o.Budget <= 0 {
		return DefaultBudget
	}

	return o.Budget
}

func (o *Options) probeTimeout() time.Duration {
	if o.ProbeTimeout <= 0 {
		return DefaultProbeTimeout
	}

	return o.ProbeTimeout
}

func (o *Options) maxSilentHops() int {
	if o.MaxSilentHops <= 0 {
		return DefaultMaxSilentHops
	}

	return o.MaxSilentHops
}

func (o *Options) resolveTimeout() time.Duration {
	if o.ResolveTimeout <= 0 {
		return DefaultResolveTimeout
	}

	return o.ResolveTimeout
}

func (o *Options) now() time.Time {
	if o.Now == nil {
		return time.Now()
	}

	return o.Now()
}

// Trace walks the TTLs, aggregates the rounds, and returns the capture.
//
// It returns an error ONLY when it could not start (no target). A trace that
// reached nothing still returns a capture: "every hop past the third is silent"
// is exactly the evidence the incident wanted, and discarding it because the
// target never answered would throw away the answer.
func Trace(ctx context.Context, prober Prober, opts *Options) (*Capture, error) {
	if opts.Address == nil {
		return nil, ErrNoTarget
	}

	start := opts.now()
	deadline := start.Add(opts.budget())

	capture := &Capture{
		Mode:                prober.Mode(),
		Host:                opts.Host,
		Address:             opts.Address.String(),
		Family:              familyOf(opts.Address),
		Port:                opts.Port,
		Rounds:              opts.rounds(),
		MaxHops:             opts.maxHops(),
		StartedAt:           start.UTC(),
		HopAddressesVisible: prober.Mode() != ModeTCP,
	}

	state := newSweepState(opts.maxHops())
	seq := 0

	for round := range opts.rounds() {
		if opts.now().After(deadline) {
			capture.Truncated = true

			break
		}

		seq = state.sweep(ctx, prober, opts, deadline, seq, capture)

		if capture.Truncated {
			break
		}

		// A round that reached the target still costs the remaining rounds; a
		// round that ran into the silent-hop cutoff with nothing at all is a
		// path we cannot see, so repeating it buys nothing but budget burn.
		if round == 0 && state.answered == 0 {
			break
		}
	}

	capture.Hops = state.hops(opts.maxHops())
	capture.Complete = state.finalTTL > 0

	resolveHops(ctx, capture, opts, deadline)

	capture.DurationMs = opts.now().Sub(start).Milliseconds()

	return capture, nil
}

// sweepState accumulates per-TTL counters across rounds.
type sweepState struct {
	buckets  []*hopBucket
	finalTTL int
	answered int
}

type hopBucket struct {
	sent        int
	received    int
	addresses   []string
	rttTotal    time.Duration
	rttMin      time.Duration
	rttMax      time.Duration
	final       bool
	unreachable bool
}

func newSweepState(maxHops int) *sweepState {
	buckets := make([]*hopBucket, maxHops+1)
	for i := range buckets {
		buckets[i] = &hopBucket{}
	}

	return &sweepState{buckets: buckets}
}

// sweep runs one round of TTLs and returns the next sequence number.
func (s *sweepState) sweep(
	ctx context.Context, prober Prober, opts *Options,
	deadline time.Time, seq int, capture *Capture,
) int {
	silent := 0

	limit := opts.maxHops()
	if s.finalTTL > 0 {
		limit = s.finalTTL
	}

	for ttl := 1; ttl <= limit; ttl++ {
		remaining := deadline.Sub(opts.now())
		if remaining <= 0 {
			capture.Truncated = true

			return seq
		}

		if ctx.Err() != nil {
			capture.Truncated = true

			return seq
		}

		seq++

		reply, err := s.probeOnce(ctx, prober, opts, remaining, ttl, seq)

		bucket := s.buckets[ttl]
		bucket.sent++

		if err != nil {
			silent++
			if silent >= opts.maxSilentHops() && s.finalTTL == 0 {
				return seq
			}

			continue
		}

		silent = 0
		s.answered++
		s.record(bucket, reply)

		if reply.Final {
			s.finalTTL = ttl

			return seq
		}

		if reply.Unreachable {
			// A router refused to forward. Nothing past it will answer, and
			// walking on would only manufacture silent hops.
			return seq
		}
	}

	return seq
}

func (s *sweepState) probeOnce(
	ctx context.Context, prober Prober, opts *Options,
	remaining time.Duration, ttl, seq int,
) (Reply, error) {
	timeout := opts.probeTimeout()
	if remaining < timeout {
		timeout = remaining
	}

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return prober.Probe(probeCtx, opts.Address, ttl, seq)
}

func (s *sweepState) record(bucket *hopBucket, reply Reply) {
	bucket.received++

	if bucket.received == 1 || reply.RTT < bucket.rttMin {
		bucket.rttMin = reply.RTT
	}

	if reply.RTT > bucket.rttMax {
		bucket.rttMax = reply.RTT
	}

	bucket.rttTotal += reply.RTT

	if reply.Final {
		bucket.final = true
	}

	if reply.Unreachable {
		bucket.unreachable = true
	}

	if reply.From == nil {
		return
	}

	addr := reply.From.String()
	for _, known := range bucket.addresses {
		if known == addr {
			return
		}
	}

	// ECMP means one TTL can legitimately answer from several routers. Keeping
	// every distinct address (rather than the first, or the last) is what stops
	// a load-balanced path from looking like a flapping one.
	bucket.addresses = append(bucket.addresses, addr)
}

// hops renders the accumulated buckets, trimming the trailing TTLs that were
// never probed.
func (s *sweepState) hops(maxHops int) []Hop {
	last := 0

	for ttl := 1; ttl <= maxHops; ttl++ {
		if s.buckets[ttl].sent > 0 {
			last = ttl
		}
	}

	out := make([]Hop, 0, last)

	for ttl := 1; ttl <= last; ttl++ {
		out = append(out, s.buckets[ttl].render(ttl))
	}

	return out
}

func (b *hopBucket) render(ttl int) Hop {
	hop := Hop{
		TTL:         ttl,
		Sent:        b.sent,
		Received:    b.received,
		Final:       b.final,
		Unreachable: b.unreachable,
	}

	if b.sent > 0 {
		hop.LossPct = roundPct(float64(b.sent-b.received) / float64(b.sent) * 100)
	}

	if len(b.addresses) > 0 {
		sorted := append([]string(nil), b.addresses...)
		sort.Strings(sorted)
		hop.Address = b.addresses[0]

		if len(sorted) > 1 {
			hop.Addresses = sorted
		}
	}

	if b.received > 0 {
		hop.RTTMinMs = millis(b.rttMin)
		hop.RTTMaxMs = millis(b.rttMax)
		hop.RTTAvgMs = millis(b.rttTotal / time.Duration(b.received))
	}

	return hop
}

// resolveHops fills in PTR names, inside whatever is left of the budget.
//
// Names are strictly a nicety, so this stops the moment the budget is spent and
// leaves the remaining hops unnamed rather than extending the capture.
func resolveHops(ctx context.Context, capture *Capture, opts *Options, deadline time.Time) {
	if opts.Resolver == nil {
		return
	}

	seen := map[string]string{}

	for idx := range capture.Hops {
		addr := capture.Hops[idx].Address
		if addr == "" {
			continue
		}

		if name, ok := seen[addr]; ok {
			capture.Hops[idx].Hostname = name

			continue
		}

		remaining := deadline.Sub(opts.now())
		if remaining <= 0 || ctx.Err() != nil {
			return
		}

		timeout := opts.resolveTimeout()
		if remaining < timeout {
			timeout = remaining
		}

		name := lookupOne(ctx, opts.Resolver, addr, timeout)
		seen[addr] = name
		capture.Hops[idx].Hostname = name
	}
}

func lookupOne(ctx context.Context, resolver Resolver, addr string, timeout time.Duration) string {
	lookupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	names, err := resolver.LookupAddr(lookupCtx, addr)
	if err != nil || len(names) == 0 {
		return ""
	}

	return trimTrailingDot(names[0])
}

func trimTrailingDot(name string) string {
	if len(name) > 1 && name[len(name)-1] == '.' {
		return name[:len(name)-1]
	}

	return name
}

func familyOf(ip net.IP) string {
	if ip.To4() != nil {
		return "ipv4"
	}

	return "ipv6"
}

func millis(d time.Duration) float64 {
	return roundPct(float64(d.Microseconds()) / 1000.0)
}

// roundPct rounds to two decimals. Traces are read by humans; a hop that took
// 12.3456789 ms took 12.35 ms.
func roundPct(value float64) float64 {
	return float64(int64(value*100+0.5)) / 100
}
