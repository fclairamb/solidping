package nettrace_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/nettrace"
)

// fakeProber is the whole reason the round/aggregation logic lives behind an
// interface: every behavior below is pinned without a socket, a privilege, or
// a network, so it runs identically on a laptop and in a CI container.
type fakeProber struct {
	mode nettrace.Mode
	// path maps TTL -> the reply that TTL produces. A TTL absent from the map
	// is silent (ErrProbeTimeout), which is how a real black hole looks.
	path map[int]nettrace.Reply
	// rttByCall, when set for a TTL, overrides the RTT per successive call so
	// min/avg/max can be given known, distinct values.
	rttByCall map[int][]time.Duration
	calls     map[int]int
	// clock is advanced by tick on every probe, so budget behavior is exact
	// rather than timing-dependent.
	clock  *time.Time
	tick   time.Duration
	closed bool
}

func newFakeProber(mode nettrace.Mode, path map[int]nettrace.Reply) *fakeProber {
	return &fakeProber{mode: mode, path: path, calls: map[int]int{}}
}

func (f *fakeProber) Mode() nettrace.Mode { return f.mode }

func (f *fakeProber) Probe(_ context.Context, _ net.IP, ttl, _ int) (nettrace.Reply, error) {
	call := f.calls[ttl]
	f.calls[ttl]++

	if f.clock != nil {
		*f.clock = f.clock.Add(f.tick)
	}

	reply, ok := f.path[ttl]
	if !ok {
		return nettrace.Reply{}, nettrace.ErrProbeTimeout
	}

	if rtts, has := f.rttByCall[ttl]; has && call < len(rtts) {
		reply.RTT = rtts[call]
	}

	return reply, nil
}

func (f *fakeProber) Close() error {
	f.closed = true

	return nil
}

const traceTarget = "192.0.2.10"

var errResolverDown = errors.New("resolver down")

func hop(addr string, rtt time.Duration) nettrace.Reply {
	return nettrace.Reply{From: net.ParseIP(addr), RTT: rtt}
}

func final(rtt time.Duration) nettrace.Reply {
	return nettrace.Reply{From: net.ParseIP(traceTarget), RTT: rtt, Final: true}
}

func baseOpts() *nettrace.Options {
	return &nettrace.Options{
		Host:          "acme.com",
		Address:       net.ParseIP(traceTarget),
		Port:          443,
		Rounds:        3,
		MaxHops:       10,
		Budget:        15 * time.Second,
		ProbeTimeout:  time.Second,
		MaxSilentHops: 5,
	}
}

func TestTraceReachesTheTargetAndStops(t *testing.T) {
	t.Parallel()

	prober := newFakeProber(nettrace.ModeICMPRaw, map[int]nettrace.Reply{
		1: hop("10.0.0.1", 1*time.Millisecond),
		2: hop("10.1.0.1", 5*time.Millisecond),
		3: final(20 * time.Millisecond),
	})

	capture, err := nettrace.Trace(t.Context(), prober, baseOpts())
	if err != nil {
		t.Fatalf("trace: %v", err)
	}

	if !capture.Complete {
		t.Fatalf("expected a complete capture")
	}

	if capture.Truncated {
		t.Fatalf("a complete trace must not be marked truncated")
	}

	if len(capture.Hops) != 3 {
		t.Fatalf("hops = %d, want 3 (the walk must stop at the target)", len(capture.Hops))
	}

	// MaxHops is 10; without the stop-at-target rule the sweep would have
	// probed TTLs 4..10 as well, three times over.
	if prober.calls[4] != 0 {
		t.Fatalf("probed past the target: TTL 4 called %d times", prober.calls[4])
	}

	if !capture.Hops[2].Final {
		t.Fatalf("the last hop is not marked final: %+v", capture.Hops[2])
	}

	for idx, got := range capture.Hops {
		if got.TTL != idx+1 {
			t.Fatalf("hop %d has TTL %d — the list must be dense and 1-based", idx, got.TTL)
		}

		if got.Sent != 3 {
			t.Fatalf("hop %d sent %d probes, want one per round", got.TTL, got.Sent)
		}
	}
}

func TestTraceAggregatesLossAndRTT(t *testing.T) {
	t.Parallel()

	prober := newFakeProber(nettrace.ModeICMPRaw, map[int]nettrace.Reply{
		1: hop("10.0.0.1", 0),
		2: final(0),
	})
	prober.rttByCall = map[int][]time.Duration{
		1: {10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond},
		2: {5 * time.Millisecond, 7 * time.Millisecond, 9 * time.Millisecond},
	}

	opts := baseOpts()
	opts.MaxHops = 5

	capture, err := nettrace.Trace(t.Context(), prober, opts)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}

	first := capture.Hops[0]
	if first.RTTMinMs != 10 || first.RTTAvgMs != 20 || first.RTTMaxMs != 30 {
		t.Fatalf("hop 1 rtt min/avg/max = %v/%v/%v, want 10/20/30",
			first.RTTMinMs, first.RTTAvgMs, first.RTTMaxMs)
	}

	if first.LossPct != 0 {
		t.Fatalf("hop 1 loss = %v, want 0", first.LossPct)
	}

	if first.Sent != 3 || first.Received != 3 {
		t.Fatalf("hop 1 sent/received = %d/%d, want 3/3", first.Sent, first.Received)
	}
}

func TestTraceRecordsPartialLoss(t *testing.T) {
	t.Parallel()

	// TTL 2 answers only on the first of three rounds.
	prober := &flakyProber{
		answersAtTTL2: 1,
		fakeProber:    newFakeProber(nettrace.ModeICMPRaw, map[int]nettrace.Reply{1: hop("10.0.0.1", time.Millisecond)}),
	}

	opts := baseOpts()
	opts.MaxHops = 3

	capture, err := nettrace.Trace(t.Context(), prober, opts)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}

	if len(capture.Hops) < 2 {
		t.Fatalf("expected at least two hops, got %d", len(capture.Hops))
	}

	second := capture.Hops[1]
	if second.Sent != 3 || second.Received != 1 {
		t.Fatalf("hop 2 sent/received = %d/%d, want 3/1", second.Sent, second.Received)
	}

	// 2 lost of 3 = 66.67%, and the two-decimal rounding is part of the
	// contract because these numbers are read by humans.
	if second.LossPct != 66.67 {
		t.Fatalf("hop 2 loss = %v, want 66.67", second.LossPct)
	}
}

// flakyProber answers TTL 2 only for the first N calls, so partial loss is
// deterministic rather than timing-dependent.
type flakyProber struct {
	*fakeProber

	answersAtTTL2 int
	seenTTL2      int
}

func (f *flakyProber) Probe(ctx context.Context, dst net.IP, ttl, seq int) (nettrace.Reply, error) {
	if ttl == 2 {
		f.seenTTL2++
		if f.seenTTL2 <= f.answersAtTTL2 {
			return hop("10.1.0.1", 4*time.Millisecond), nil
		}

		return nettrace.Reply{}, nettrace.ErrProbeTimeout
	}

	return f.fakeProber.Probe(ctx, dst, ttl, seq)
}

func TestTraceStopsAfterConsecutiveSilentHops(t *testing.T) {
	t.Parallel()

	// A black hole after hop 2: nothing past it ever answers.
	prober := newFakeProber(nettrace.ModeICMPRaw, map[int]nettrace.Reply{
		1: hop("10.0.0.1", time.Millisecond),
		2: hop("10.1.0.1", 3*time.Millisecond),
	})

	opts := baseOpts()
	opts.MaxHops = 30
	opts.MaxSilentHops = 3

	capture, err := nettrace.Trace(t.Context(), prober, opts)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}

	if capture.Complete {
		t.Fatalf("a black-holed path must not report complete")
	}

	// The cutoff is the point of this test: without it the sweep walks all 30
	// TTLs, three times, at a second each — three times the budget spent
	// learning nothing hop 2 did not already say.
	if prober.calls[6] != 0 {
		t.Fatalf("probed TTL 6, past the silent-hop cutoff (%d calls)", prober.calls[6])
	}

	if len(capture.Hops) != 5 {
		t.Fatalf("hops = %d, want 5 (two answered plus three silent)", len(capture.Hops))
	}

	last := capture.Hops[4]
	if last.Received != 0 || last.LossPct != 100 {
		t.Fatalf("the trailing silent hop is not 100%% loss: %+v", last)
	}
}

func TestTraceStopsAtAnUnreachableRouter(t *testing.T) {
	t.Parallel()

	prober := newFakeProber(nettrace.ModeICMPRaw, map[int]nettrace.Reply{
		1: hop("10.0.0.1", time.Millisecond),
		2: {From: net.ParseIP("10.1.0.1"), RTT: 2 * time.Millisecond, Unreachable: true},
	})

	opts := baseOpts()
	opts.MaxHops = 20

	capture, err := nettrace.Trace(t.Context(), prober, opts)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}

	if prober.calls[3] != 0 {
		t.Fatalf("kept probing past an explicit unreachable")
	}

	if !capture.Hops[1].Unreachable {
		t.Fatalf("the unreachable hop is not flagged: %+v", capture.Hops[1])
	}

	if capture.Complete {
		t.Fatalf("an unreachable is not a completed path")
	}
}

func TestTraceHonoursTheBudget(t *testing.T) {
	t.Parallel()

	clock := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	// Each probe burns a full second of the clock.
	prober := newFakeProber(nettrace.ModeICMPRaw, map[int]nettrace.Reply{})
	prober.clock = &clock
	prober.tick = time.Second

	opts := baseOpts()
	opts.MaxHops = 30
	opts.MaxSilentHops = 30
	opts.Budget = 4 * time.Second
	opts.Now = func() time.Time { return clock }

	capture, err := nettrace.Trace(t.Context(), prober, opts)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}

	if !capture.Truncated {
		t.Fatalf("a budget-exhausted trace must say so")
	}

	total := 0
	for _, count := range prober.calls {
		total += count
	}

	// Four seconds of budget at a second a probe. One or two extra would mean
	// the deadline is checked after the probe rather than before it.
	if total > 5 {
		t.Fatalf("sent %d probes on a 4s budget — the deadline is not being enforced", total)
	}
}

func TestTraceRequiresATarget(t *testing.T) {
	t.Parallel()

	prober := newFakeProber(nettrace.ModeICMPRaw, nil)

	if _, err := nettrace.Trace(t.Context(), prober, &nettrace.Options{}); !errors.Is(err, nettrace.ErrNoTarget) {
		t.Fatalf("err = %v, want ErrNoTarget", err)
	}
}

func TestTraceRecordsEveryDistinctAddressAtAHop(t *testing.T) {
	t.Parallel()

	// ECMP: TTL 1 answers from two different routers across the rounds.
	prober := &ecmpProber{fakeProber: newFakeProber(nettrace.ModeICMPRaw, map[int]nettrace.Reply{
		2: final(9 * time.Millisecond),
	})}

	opts := baseOpts()
	opts.MaxHops = 4

	capture, err := nettrace.Trace(t.Context(), prober, opts)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}

	first := capture.Hops[0]
	if len(first.Addresses) != 2 {
		t.Fatalf("addresses = %v, want both ECMP routers", first.Addresses)
	}

	if first.Address == "" {
		t.Fatalf("Address must always be populated when any router answered")
	}
}

type ecmpProber struct {
	*fakeProber

	seen int
}

func (e *ecmpProber) Probe(ctx context.Context, dst net.IP, ttl, seq int) (nettrace.Reply, error) {
	if ttl == 1 {
		e.seen++
		if e.seen%2 == 0 {
			return hop("10.0.0.2", 2*time.Millisecond), nil
		}

		return hop("10.0.0.1", time.Millisecond), nil
	}

	return e.fakeProber.Probe(ctx, dst, ttl, seq)
}

func TestTraceResolvesHopNames(t *testing.T) {
	t.Parallel()

	prober := newFakeProber(nettrace.ModeICMPRaw, map[int]nettrace.Reply{
		1: hop("10.0.0.1", time.Millisecond),
		2: final(9 * time.Millisecond),
	})

	resolver := &fakeResolver{names: map[string][]string{
		"10.0.0.1":   {"gw.acme.com."},
		"192.0.2.10": {"edge.acme.com."},
	}}

	opts := baseOpts()
	opts.MaxHops = 4
	opts.Resolver = resolver

	capture, err := nettrace.Trace(t.Context(), prober, opts)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}

	if capture.Hops[0].Hostname != "gw.acme.com" {
		t.Fatalf("hop 1 hostname = %q, want the PTR with its trailing dot trimmed", capture.Hops[0].Hostname)
	}

	if capture.Hops[1].Hostname != "edge.acme.com" {
		t.Fatalf("hop 2 hostname = %q", capture.Hops[1].Hostname)
	}

	// Two distinct addresses, so exactly two lookups: the per-address cache is
	// what keeps a 30-hop trace from spending its budget on DNS.
	if resolver.calls != 2 {
		t.Fatalf("resolver called %d times, want 2", resolver.calls)
	}
}

// TestTraceSurvivesAResolverThatFails is the honesty control on reverse DNS: a
// broken resolver costs the capture its NAMES, never its hops.
func TestTraceSurvivesAResolverThatFails(t *testing.T) {
	t.Parallel()

	prober := newFakeProber(nettrace.ModeICMPRaw, map[int]nettrace.Reply{
		1: final(time.Millisecond),
	})

	opts := baseOpts()
	opts.MaxHops = 4
	opts.Resolver = &fakeResolver{err: errResolverDown}

	capture, err := nettrace.Trace(t.Context(), prober, opts)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}

	if len(capture.Hops) != 1 || capture.Hops[0].Address != "192.0.2.10" {
		t.Fatalf("a failing resolver damaged the hop list: %+v", capture.Hops)
	}

	if capture.Hops[0].Hostname != "" {
		t.Fatalf("hostname = %q, want empty", capture.Hops[0].Hostname)
	}
}

type fakeResolver struct {
	names map[string][]string
	err   error
	calls int
}

func (f *fakeResolver) LookupAddr(_ context.Context, addr string) ([]string, error) {
	f.calls++

	if f.err != nil {
		return nil, f.err
	}

	return f.names[addr], nil
}

// TestCaptureJSONShape pins the wire contract the dashboard and the stored
// attachment both depend on.
func TestCaptureJSONShape(t *testing.T) {
	t.Parallel()

	prober := newFakeProber(nettrace.ModeICMPUDP, map[int]nettrace.Reply{
		1: hop("10.0.0.1", 1500*time.Microsecond),
		2: final(12 * time.Millisecond),
	})

	opts := baseOpts()
	opts.MaxHops = 4
	opts.Rounds = 1

	capture, err := nettrace.Trace(t.Context(), prober, opts)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}

	capture.Region = "eu2"
	capture.Trigger = "incident-open"

	body, err := capture.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if unmarshalErr := json.Unmarshal(body, &decoded); unmarshalErr != nil {
		t.Fatalf("unmarshal: %v", unmarshalErr)
	}

	for _, key := range []string{
		"version", "mode", "hopAddressesVisible", "host", "address", "family",
		"port", "region", "trigger", "rounds", "maxHops", "startedAt",
		"durationMs", "complete", "hops",
	} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("capture JSON is missing %q: %s", key, body)
		}
	}

	hops, ok := decoded["hops"].([]any)
	if !ok || len(hops) != 2 {
		t.Fatalf("hops did not serialize as a 2-element array: %v", decoded["hops"])
	}

	firstHop, ok := hops[0].(map[string]any)
	if !ok {
		t.Fatalf("hop is not an object: %v", hops[0])
	}

	for _, key := range []string{"ttl", "address", "sent", "received", "lossPct", "rttAvgMs"} {
		if _, has := firstHop[key]; !has {
			t.Fatalf("hop JSON is missing %q: %v", key, firstHop)
		}
	}

	// Round-trip through the strict parser the upload endpoint uses.
	reparsed, err := nettrace.ParseCapture(body)
	if err != nil {
		t.Fatalf("the capture we produce fails the parser we accept with: %v", err)
	}

	if reparsed.Mode != nettrace.ModeICMPUDP || !reparsed.HopAddressesVisible {
		t.Fatalf("round-trip lost the mode: %+v", reparsed)
	}
}

func TestParseCaptureRejectsJunk(t *testing.T) {
	t.Parallel()

	valid := &nettrace.Capture{
		Mode:                nettrace.ModeTCP,
		HopAddressesVisible: false,
		Address:             "192.0.2.10",
		Family:              "ipv4",
		Hops:                []nettrace.Hop{{TTL: 1, Sent: 1}},
	}

	body, err := valid.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Positive control: the shape we mint is accepted, so every rejection
	// below is about the mutation and not about the parser being broken.
	if _, parseErr := nettrace.ParseCapture(body); parseErr != nil {
		t.Fatalf("valid capture rejected: %v", parseErr)
	}

	cases := map[string][]byte{
		"empty":            {},
		"not json":         []byte("PNGnot-json"),
		"an array":         []byte(`[]`),
		"no version":       []byte(`{"mode":"icmp","address":"192.0.2.10","family":"ipv4","hops":[]}`),
		"unknown mode":     []byte(`{"version":1,"mode":"quantum","address":"1.2.3.4","family":"ipv4","hops":[]}`),
		"no address":       []byte(`{"version":1,"mode":"icmp","family":"ipv4","hops":[]}`),
		"unknown field":    []byte(`{"version":1,"mode":"icmp","address":"1.2.3.4","family":"ipv4","hops":[],"exec":"rm"}`),
		"future version":   []byte(`{"version":99,"mode":"icmp","address":"1.2.3.4","family":"ipv4","hops":[]}`),
		"a bare png magic": {0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := nettrace.ParseCapture(body); !errors.Is(err, nettrace.ErrInvalidCapture) {
				t.Fatalf("ParseCapture(%q) err = %v, want ErrInvalidCapture", name, err)
			}
		})
	}
}
