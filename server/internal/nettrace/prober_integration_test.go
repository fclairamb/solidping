package nettrace_test

import (
	"net"
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/nettrace"
)

// The integration tests below drive REAL sockets. The spec is explicit that
// "flaky-by-privilege is not acceptable; the gate must be deterministic", so
// each one is gated on nettrace.Detect() — the same socket-open probe the
// production path uses — rather than on a guess about uid, container runtime or
// sysctl. A host that cannot open the socket skips; a host that can must pass.
// There is no third outcome.

func TestCapabilityModeLadder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cap  nettrace.Capability
		isV6 bool
		port int
		want nettrace.Mode
	}{
		{
			"raw wins when available",
			nettrace.Capability{ICMPRawV4: true, ICMPUDPV4: true},
			false, 443, nettrace.ModeICMPRaw,
		},
		{
			"unprivileged icmp is the second rung",
			nettrace.Capability{ICMPUDPV4: true},
			false, 443, nettrace.ModeICMPUDP,
		},
		{
			"tcp is the last resort",
			nettrace.Capability{},
			false, 443, nettrace.ModeTCP,
		},
		{
			"no icmp and no port means no trace at all",
			nettrace.Capability{},
			false, 0, "",
		},
		{
			"v6 capability does not satisfy a v4 target",
			nettrace.Capability{ICMPRawV6: true, ICMPUDPV6: true},
			false, 0, "",
		},
		{
			"v4 capability does not satisfy a v6 target",
			nettrace.Capability{ICMPRawV4: true, ICMPUDPV4: true},
			true, 0, "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := test.cap.ModeFor(test.isV6, test.port); got != test.want {
				t.Fatalf("ModeFor(%v, %d) = %q, want %q", test.isV6, test.port, got, test.want)
			}
		})
	}
}

// TestTCPProberReachesALocalListener is the one real-socket test that needs NO
// privilege at all, so it runs everywhere including CI containers. It proves
// the TCP ladder actually connects and reports the target as final.
func TestTCPProberReachesALocalListener(t *testing.T) {
	t.Parallel()

	port := acceptingListener(t)

	prober, err := nettrace.NewProber(nettrace.ModeTCP, false, port)
	if err != nil {
		t.Fatalf("new prober: %v", err)
	}

	defer func() { _ = prober.Close() }()

	capture, err := nettrace.Trace(t.Context(), prober, &nettrace.Options{
		Host:         "localhost",
		Address:      net.ParseIP("127.0.0.1"),
		Port:         port,
		Rounds:       2,
		MaxHops:      5,
		Budget:       5 * time.Second,
		ProbeTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("trace: %v", err)
	}

	if !capture.Complete {
		t.Fatalf("loopback trace did not complete: %+v", capture)
	}

	if capture.HopAddressesVisible {
		t.Fatalf("the TCP mode must declare that it cannot see hop addresses")
	}

	if len(capture.Hops) != 1 || !capture.Hops[0].Final {
		t.Fatalf("expected exactly one final hop on loopback, got %+v", capture.Hops)
	}
}

// TestTCPProberNeedsAPort pins the one configuration that cannot fall back:
// an ICMP check has no port, so on a host with no ICMP socket there is simply
// nothing to trace with.
func TestTCPProberNeedsAPort(t *testing.T) {
	t.Parallel()

	if _, err := nettrace.NewProber(nettrace.ModeTCP, false, 0); err == nil {
		t.Fatalf("a TCP prober with no port was accepted")
	}
}

// TestICMPProberAgainstLoopback exercises the real ICMP socket path — echo
// construction, TTL setting, reply matching — against 127.0.0.1.
func TestICMPProberAgainstLoopback(t *testing.T) {
	t.Parallel()

	capability := nettrace.Detect()

	mode := capability.ModeFor(false, 0)
	if mode == "" {
		t.Skip("no ICMP socket available on this host (unprivileged container): " +
			"the ICMP path cannot be exercised here")
	}

	prober, err := nettrace.NewProber(mode, false, 0)
	if err != nil {
		t.Fatalf("new prober: %v", err)
	}

	defer func() { _ = prober.Close() }()

	capture, err := nettrace.Trace(t.Context(), prober, &nettrace.Options{
		Host:         "localhost",
		Address:      net.ParseIP("127.0.0.1"),
		Rounds:       2,
		MaxHops:      5,
		Budget:       5 * time.Second,
		ProbeTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("trace: %v", err)
	}

	if !capture.Complete {
		t.Fatalf("loopback is one hop away and must always complete: %+v", capture)
	}

	if !capture.HopAddressesVisible {
		t.Fatalf("an ICMP mode must declare hop addresses visible")
	}

	if len(capture.Hops) != 1 {
		t.Fatalf("expected one hop to loopback, got %d: %+v", len(capture.Hops), capture.Hops)
	}

	hop := capture.Hops[0]
	if !hop.Final || hop.Received != 2 || hop.LossPct != 0 {
		t.Fatalf("loopback hop is not a clean two-for-two: %+v", hop)
	}

	if hop.Address != "127.0.0.1" {
		t.Fatalf("hop address = %q, want 127.0.0.1", hop.Address)
	}
}

// TestRunPicksAModeAndProducesACapture drives the full convenience entry point
// the dispatcher calls.
func TestRunPicksAModeAndProducesACapture(t *testing.T) {
	t.Parallel()

	port := acceptingListener(t)

	capture, err := nettrace.Run(t.Context(), &nettrace.Options{
		Host:         "localhost",
		Address:      net.ParseIP("127.0.0.1"),
		Port:         port,
		Rounds:       1,
		MaxHops:      4,
		Budget:       5 * time.Second,
		ProbeTimeout: time.Second,
		// No resolver override: Run installs net.DefaultResolver, and loopback
		// either has a PTR or does not. Either way the hops stand.
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if !capture.Complete || len(capture.Hops) == 0 {
		t.Fatalf("Run produced nothing usable: %+v", capture)
	}

	// Whichever rung the ladder landed on, the capture must name it.
	switch capture.Mode {
	case nettrace.ModeICMPRaw, nettrace.ModeICMPUDP, nettrace.ModeTCP:
	default:
		t.Fatalf("capture carries an unknown mode %q", capture.Mode)
	}

	if _, err := capture.Marshal(); err != nil {
		t.Fatalf("a real capture does not serialize: %v", err)
	}
}

// TestRunWithoutATargetIsAnError — a trace with nothing to trace to is a
// programming error, not a silent no-op.
func TestRunWithoutATargetIsAnError(t *testing.T) {
	t.Parallel()

	if _, err := nettrace.Run(t.Context(), &nettrace.Options{}); err == nil {
		t.Fatalf("Run accepted a capture with no target")
	}
}

// acceptingListener binds a loopback port that accepts and immediately closes,
// and tears it down with the test.
func acceptingListener(t *testing.T) int {
	t.Helper()

	var config net.ListenConfig

	listener, err := config.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address is not TCP: %T", listener.Addr())
	}

	return addr.Port
}
