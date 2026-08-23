package checkssl

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestRefusedTLSConnectCarriesAMarker: nothing listening means the TCP
// connection never came up, which is a path problem and therefore traceable.
func TestRefusedTLSConnectCarriesAMarker(t *testing.T) {
	t.Parallel()

	port := closedLoopbackPort(t)

	checker := &SSLChecker{}
	cfg := &SSLConfig{Host: "127.0.0.1", Port: port}

	result, err := checker.Execute(t.Context(), cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if result.Diagnostics == nil || result.Diagnostics.NetworkFailure == nil {
		t.Fatalf("a refused TLS connect carried no marker: %+v", result.Diagnostics)
	}

	failure := result.Diagnostics.NetworkFailure
	if failure.Class != checkerdef.NetFailureConnectionRefused {
		t.Fatalf("class = %q, want %q", failure.Class, checkerdef.NetFailureConnectionRefused)
	}

	if failure.Address != "127.0.0.1" || failure.Port != port {
		t.Fatalf("marker endpoint = %s:%d, want 127.0.0.1:%d", failure.Address, failure.Port, port)
	}
}

// TestCertificateVerdictCarriesNoMarker is the negative the spec names
// explicitly: a certificate problem is the server ANSWERING. The connection
// came up, the handshake reached a verdict, and the path is provably fine — so
// a traceroute would be noise on the incident.
//
// httptest's TLS server presents a self-signed certificate, so the handshake
// fails on verification with the connection established. That is the exact
// shape of "cert expiry" for this code path.
func TestCertificateVerdictCarriesNoMarker(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	host, port := splitLoopback(t, server.Listener.Addr())

	checker := &SSLChecker{}
	cfg := &SSLConfig{Host: host, Port: port}

	result, err := checker.Execute(t.Context(), cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Positive control on the OUTCOME: this really is a failing check, so the
	// missing marker cannot be explained by "nothing failed".
	if result.Status == checkerdef.StatusUp {
		t.Fatalf("expected the self-signed certificate to fail the check, got %v (%v)",
			result.Status, result.Output)
	}

	if result.Diagnostics != nil && result.Diagnostics.NetworkFailure != nil {
		t.Fatalf("a certificate verdict minted a marker: %+v", result.Diagnostics.NetworkFailure)
	}
}

// TestHealthyTLSCheckCarriesNoMarker — the other control: a passing check never
// carries one either.
func TestHealthyTLSCheckCarriesNoMarker(t *testing.T) {
	t.Parallel()

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	server.StartTLS()

	defer server.Close()

	host, port := splitLoopback(t, server.Listener.Addr())

	checker := &SSLChecker{}
	cfg := &SSLConfig{Host: host, Port: port}

	result, err := checker.Execute(t.Context(), cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if result.Diagnostics != nil && result.Diagnostics.NetworkFailure != nil {
		t.Fatalf("a TLS check carried a marker it should not: %+v", result.Diagnostics.NetworkFailure)
	}
}

func splitLoopback(t *testing.T, addr net.Addr) (string, int) {
	t.Helper()

	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address is not TCP: %T", addr)
	}

	return "127.0.0.1", tcpAddr.Port
}

func closedLoopbackPort(t *testing.T) int {
	t.Helper()

	var config net.ListenConfig

	listener, err := config.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	_, port := splitLoopback(t, listener.Addr())

	if closeErr := listener.Close(); closeErr != nil {
		t.Fatalf("close: %v", closeErr)
	}

	return port
}
