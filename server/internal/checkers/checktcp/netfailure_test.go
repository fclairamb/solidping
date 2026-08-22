package checktcp

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// closedPort binds and immediately releases a loopback port, so a connect to it
// is refused rather than dropped. Racy in theory, deterministic in practice on
// a loopback interface, and the alternative (a hard-coded port) is worse.
func closedPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port

	if closeErr := listener.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}

	return port
}

// TestRefusedConnectionCarriesANetworkFailureMarker is the trigger's
// foundation: without this marker the incident pipeline can never decide to
// trace, so a checker that stops setting it silently disables the feature.
func TestRefusedConnectionCarriesANetworkFailureMarker(t *testing.T) {
	t.Parallel()

	port := closedPort(t)

	checker := &TCPChecker{}
	cfg := &TCPConfig{Host: "127.0.0.1", Port: port, Timeout: 2 * time.Second}

	result, err := checker.Execute(t.Context(), cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if result.Status == checkerdef.StatusUp {
		t.Fatalf("expected the probe to fail, got %v", result.Status)
	}

	if result.Diagnostics == nil || result.Diagnostics.NetworkFailure == nil {
		t.Fatalf("a refused connection carried no network-failure marker: %+v", result.Diagnostics)
	}

	failure := result.Diagnostics.NetworkFailure
	if failure.Class != checkerdef.NetFailureConnectionRefused {
		t.Fatalf("class = %q, want %q", failure.Class, checkerdef.NetFailureConnectionRefused)
	}

	// The endpoint is what the trace will be pointed at. An empty address here
	// means the feature runs but traces nothing.
	if failure.Address != "127.0.0.1" || failure.Port != port {
		t.Fatalf("marker endpoint = %s:%d, want 127.0.0.1:%d", failure.Address, failure.Port, port)
	}
}

// TestSuccessfulProbeCarriesNoNetworkFailureMarker is the negative, with the
// refusal test above as its positive control: the marker exists only on the
// failure path, so it can never make a healthy check look traceable.
func TestSuccessfulProbeCarriesNoNetworkFailureMarker(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	defer func() { _ = listener.Close() }()

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	checker := &TCPChecker{}
	cfg := &TCPConfig{
		Host:    "127.0.0.1",
		Port:    listener.Addr().(*net.TCPAddr).Port,
		Timeout: 2 * time.Second,
	}

	result, err := checker.Execute(t.Context(), cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if result.Status != checkerdef.StatusUp {
		t.Fatalf("expected the probe to succeed, got %v (%v)", result.Status, result.Output)
	}

	if result.Diagnostics != nil && result.Diagnostics.NetworkFailure != nil {
		t.Fatalf("a successful probe carried a network-failure marker: %+v", result.Diagnostics.NetworkFailure)
	}
}

// TestTunneledFailureCarriesNoNetworkFailureMarker pins the deliberate hole: a
// probe dialed through an SSH bastion failed on the FAR side, so a trace run
// from this worker would describe a route the packet never took. No marker,
// therefore no trace, therefore no misleading hop list on the incident.
func TestTunneledFailureCarriesNoNetworkFailureMarker(t *testing.T) {
	t.Parallel()

	port := closedPort(t)

	// The "tunnel" is a plain dialer, so the failure is byte-for-byte the one
	// the direct path classifies as connection-refused above. The only
	// difference is that the checker took the tunneled branch.
	ctx := checkerdef.WithTunnelDialer(t.Context(), dialerFunc(func(
		ctx context.Context, network, address string,
	) (net.Conn, error) {
		var dialer net.Dialer

		return dialer.DialContext(ctx, network, address)
	}))

	checker := &TCPChecker{}
	cfg := &TCPConfig{Host: "127.0.0.1", Port: port, Timeout: 2 * time.Second}

	result, err := checker.Execute(ctx, cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if result.Status == checkerdef.StatusUp {
		t.Fatalf("expected the tunneled probe to fail, got %v", result.Status)
	}

	if result.Diagnostics != nil && result.Diagnostics.NetworkFailure != nil {
		t.Fatalf("a tunneled failure carried a marker: %+v", result.Diagnostics.NetworkFailure)
	}
}

type dialerFunc func(ctx context.Context, network, address string) (net.Conn, error)

func (f dialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}
