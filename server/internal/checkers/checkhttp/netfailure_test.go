package checkhttp

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

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
