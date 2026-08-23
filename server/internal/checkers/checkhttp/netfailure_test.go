package checkhttp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestHTTP500CarriesNoNetworkFailureMarker is THE negative of this feature.
//
// "Application-level failures do not trigger a trace" is the rule the spec is
// most explicit about, and a 500 is its canonical case: the check is DOWN, the
// incident opens, and the path is provably fine because the server answered.
// A traceroute here would be pure noise on the incident page.
func TestHTTP500CarriesNoNetworkFailureMarker(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	checker := &HTTPChecker{}
	cfg := &HTTPConfig{URL: server.URL, Method: http.MethodGet}

	result, err := checker.Execute(t.Context(), cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Positive control on the OUTCOME: this really is a failing check, so the
	// absence of a marker below cannot be explained by "nothing failed".
	if result.Status == checkerdef.StatusUp {
		t.Fatalf("expected the 500 to fail the check, got %v", result.Status)
	}

	if result.Diagnostics != nil && result.Diagnostics.NetworkFailure != nil {
		t.Fatalf("an HTTP 500 minted a network-failure marker: %+v", result.Diagnostics.NetworkFailure)
	}
}

// TestHTTPKeywordMismatchCarriesNoNetworkFailureMarker — the second
// application-level class the spec names. A 200 whose body is wrong is still
// the server answering.
func TestHTTPKeywordMismatchCarriesNoNetworkFailureMarker(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("all systems nominal"))
	}))
	defer server.Close()

	checker := &HTTPChecker{}
	cfg := &HTTPConfig{
		URL:        server.URL,
		Method:     http.MethodGet,
		BodyExpect: "this string is not in the body",
	}

	result, err := checker.Execute(t.Context(), cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if result.Status == checkerdef.StatusUp {
		t.Fatalf("expected the keyword mismatch to fail the check, got %v", result.Status)
	}

	if result.Diagnostics != nil && result.Diagnostics.NetworkFailure != nil {
		t.Fatalf("a keyword mismatch minted a marker: %+v", result.Diagnostics.NetworkFailure)
	}
}

// TestHTTPRefusedConnectionCarriesANetworkFailureMarker is the positive control
// for both negatives above: the same checker, on the same host, DOES mint a
// marker when nothing is listening — so the empty results above are the rule
// working, not the wiring being absent.
func TestHTTPRefusedConnectionCarriesANetworkFailureMarker(t *testing.T) {
	t.Parallel()

	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	port := tcpPort(t, listener)

	if closeErr := listener.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}

	checker := &HTTPChecker{}
	cfg := &HTTPConfig{
		URL:    fmt.Sprintf("http://127.0.0.1:%d/", port),
		Method: http.MethodGet,
	}

	result, err := checker.Execute(t.Context(), cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if result.Diagnostics == nil || result.Diagnostics.NetworkFailure == nil {
		t.Fatalf("a refused HTTP connect carried no marker: %+v", result.Diagnostics)
	}

	failure := result.Diagnostics.NetworkFailure
	if failure.Class != checkerdef.NetFailureConnectionRefused {
		t.Fatalf("class = %q, want %q", failure.Class, checkerdef.NetFailureConnectionRefused)
	}

	// The address comes from the httptrace ConnectDone hook, which is the only
	// place it is observable when the connection never comes up.
	if failure.Address != "127.0.0.1" || failure.Port != port {
		t.Fatalf("marker endpoint = %s:%d, want 127.0.0.1:%d", failure.Address, failure.Port, port)
	}

	if failure.Host != "127.0.0.1" {
		t.Fatalf("marker host = %q, want the URL hostname", failure.Host)
	}
}

// TestHTTPUnresolvableHostCarriesNoNetworkFailureMarker: a name that does not
// resolve has no address to trace to. DNS diagnostics are explicitly a
// different capture with its own spec.
func TestHTTPUnresolvableHostCarriesNoNetworkFailureMarker(t *testing.T) {
	t.Parallel()

	checker := &HTTPChecker{}
	cfg := &HTTPConfig{
		URL:    "http://this-host-does-not-exist.invalid/",
		Method: http.MethodGet,
	}

	result, err := checker.Execute(t.Context(), cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if result.Status == checkerdef.StatusUp {
		t.Fatalf("expected the unresolvable host to fail the check, got %v", result.Status)
	}

	if result.Diagnostics != nil && result.Diagnostics.NetworkFailure != nil {
		t.Fatalf("a DNS failure minted a marker: %+v", result.Diagnostics.NetworkFailure)
	}
}

// tcpPort reads a listener's bound port, checked rather than asserted.
func tcpPort(t *testing.T, listener net.Listener) int {
	t.Helper()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address is not TCP: %T", listener.Addr())
	}

	return addr.Port
}

// TestResponseStallCarriesNoNetworkFailureMarker is the correction the audit
// asked for: a target that completes the TCP handshake and then hangs is an
// APPLICATION stall, not a reachability failure.
//
// It is the sharpest case in this file because the raw fact available to the
// checker — `ctx.Err() == DeadlineExceeded` — is byte-for-byte identical to the
// one a black-holed SYN produces. Only how far the request got tells them
// apart, and getting it wrong attaches a hop list labeled `connect-timeout` to
// an outage whose path is provably fine.
func TestResponseStallCarriesNoNetworkFailureMarker(t *testing.T) {
	t.Parallel()

	stalled := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-stalled
	}))

	t.Cleanup(func() {
		close(stalled)
		server.Close()
	})

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	checker := &HTTPChecker{}
	result, err := checker.Execute(ctx, &HTTPConfig{URL: server.URL, Method: http.MethodGet})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Positive control on the OUTCOME: the check really did time out, so the
	// missing marker is the rule working and not a probe that quietly passed.
	if result.Status != checkerdef.StatusTimeout {
		t.Fatalf("expected a timeout, got %v (%v)", result.Status, result.Output)
	}

	if result.Diagnostics != nil && result.Diagnostics.NetworkFailure != nil {
		t.Fatalf("a response stall minted a %q marker: %+v",
			result.Diagnostics.NetworkFailure.Class, result.Diagnostics.NetworkFailure)
	}
}

// TestConnectTimeoutStillCarriesAMarker is the other direction, and the control
// that keeps the test above from passing for the wrong reason: a deadline that
// fires while the connection is still being established IS a reachability
// failure and must still be traced.
//
// 198.51.100.0/24 (TEST-NET-2) is reserved and unrouted, so the SYN goes
// nowhere — the same shape as a black-holed path, without needing one.
func TestConnectTimeoutStillCarriesAMarker(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()

	checker := &HTTPChecker{}
	result, err := checker.Execute(ctx, &HTTPConfig{
		URL:    "http://198.51.100.7:8080/",
		Method: http.MethodGet,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if result.Status == checkerdef.StatusUp {
		t.Fatalf("expected the unrouted address to fail, got %v", result.Status)
	}

	if result.Diagnostics == nil || result.Diagnostics.NetworkFailure == nil {
		t.Fatalf("a connect that never completed carried no marker: %+v", result.Diagnostics)
	}

	if got := result.Diagnostics.NetworkFailure.Class; got != checkerdef.NetFailureConnectTimeout {
		t.Fatalf("class = %q, want %q", got, checkerdef.NetFailureConnectTimeout)
	}
}

// TestRefineHTTPNetworkFailure drives the phase rules directly, so every branch
// is pinned without needing a network shape that produces it.
func TestRefineHTTPNetworkFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		start string
		phase requestPhase
		want  string
	}{
		{
			"nothing connected: a real connect timeout",
			checkerdef.NetFailureConnectTimeout, phaseNone,
			checkerdef.NetFailureConnectTimeout,
		},
		{
			"the last connect failed: a real connect timeout",
			checkerdef.NetFailureConnectTimeout, phaseConnectFailed,
			checkerdef.NetFailureConnectTimeout,
		},
		{
			"the handshake stalled: reclassified, still traceable",
			checkerdef.NetFailureConnectTimeout, phaseTLSHandshaking,
			checkerdef.NetFailureTLSHandshakeTimeout,
		},
		{
			"the connection came up: the marker is dropped entirely",
			checkerdef.NetFailureConnectTimeout, phaseConnected,
			"",
		},
		{
			"a refusal is never second-guessed",
			checkerdef.NetFailureConnectionRefused, phaseConnected,
			checkerdef.NetFailureConnectionRefused,
		},
		{
			"an unreachable is never second-guessed",
			checkerdef.NetFailureHostUnreachable, phaseConnected,
			checkerdef.NetFailureHostUnreachable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result := &checkerdef.Result{}
			result.SetNetworkFailure(checkerdef.NewNetworkFailure(test.start, "acme.com", "10.0.0.1", 443))

			refineHTTPNetworkFailure(result, test.phase)

			got := ""
			if result.Diagnostics != nil && result.Diagnostics.NetworkFailure != nil {
				got = result.Diagnostics.NetworkFailure.Class
			}

			if got != test.want {
				t.Fatalf("class = %q, want %q", got, test.want)
			}
		})
	}
}
