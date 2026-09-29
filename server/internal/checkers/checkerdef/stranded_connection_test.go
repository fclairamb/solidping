package checkerdef

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/egress"
)

// TestCheckTransportsAreHTTP1 pins the transport contract behind spec
// 2026-09-28-04: no check probe may run on an HTTP/2 connection.
//
// HTTP/2's handling of a request canceled by its execution budget is what
// turns one network blip into an outage — the canceled stream stays on the
// connection "until we can confirm the server is still responding", so the
// connection remains pooled and every later probe rides it. HTTP/1.1 closes the
// connection of any request that did not complete, which is the guarantee this
// contract relies on. Both shared transports (the pool under an enforcing guard
// and the pool everywhere else) must therefore be HTTP/1.1, and neither may be
// http.DefaultTransport, which negotiates HTTP/2.
func TestCheckTransportsAreHTTP1(t *testing.T) {
	t.Parallel()

	require.False(t, checkTransport.ForceAttemptHTTP2,
		"checkTransport must not attempt HTTP/2: a canceled probe would strand its connection in the pool")
	require.NotNil(t, checkTransport.TLSNextProto,
		"an explicit TLSNextProto is what stops net/http re-enabling HTTP/2 on this transport")

	// No enforcing guard (the self-hosted posture) → the shared check transport,
	// from both entry points.
	selfHosted := egress.WithGuard(context.Background(), egress.New(true))

	shared, ok := HTTPTransportFor(selfHosted, false).(*http.Transport)
	require.True(t, ok, "an untunneled check must get an *http.Transport")
	require.Same(t, checkTransport, shared,
		"an untunneled check must share the pooled HTTP/1.1 transport")

	built, ok := BuildHTTPTransport(nil, false, IPVersionAuto).(*http.Transport)
	require.True(t, ok, "BuildHTTPTransport must get an *http.Transport")
	require.Same(t, checkTransport, built,
		"BuildHTTPTransport must not fall back to http.DefaultTransport")

	// Enforcing guard (every SaaS worker) → the guard's own pooled transport,
	// which must be HTTP/1.1 for exactly the same reason.
	enforcing := egress.New(false)
	transport, ok := HTTPTransportFor(egress.WithGuard(context.Background(), enforcing), false).(*http.Transport)
	require.True(t, ok, "the enforcing path must hand out the guard's *http.Transport")
	require.Same(t, enforcing.HTTPTransport(), transport)
	require.False(t, transport.ForceAttemptHTTP2,
		"the guard's shared transport must not attempt HTTP/2 either")
}

// TestStrandedConnectionDoesNotSurviveTheProbeThatHitIt is the regression test
// for the 2026-09-28 production failure: a peer that loses connection state
// mid-blip stops answering on that connection while the path itself is fine.
// Every probe that then rides the same connection burns its whole execution
// budget without putting a packet on the wire, and the failure only ends when
// the kernel gives up on the 5-tuple (`tcp_retries2`, ~15 min).
//
// Measured on the production `lauterbourg` worker: a ~1-minute IPv6 blackhole
// cost 16 consecutive failing probes (255 failing minutes in a day, of which
// only 37 were the blip itself) and 16 `region-heartbeat-lauterbourg` incidents.
//
// The server strands its FIRST connection and answers every later one; the
// second probe must therefore answer, on a connection of its own.
func TestStrandedConnectionDoesNotSurviveTheProbeThatHitIt(t *testing.T) {
	t.Parallel()

	server, seen := strandedFirstConnServer(t)
	defer server.Close()

	// An enforcing guard with loopback declared public for this test only: the
	// enforcing transport is the one every SaaS HTTP check dials through.
	guard := egress.New(false, egress.WithPublicOverride(net.ParseIP("127.0.0.1")))
	require.True(t, guard.Enforcing())

	transport := guard.HTTPTransport()

	// Trust the test server's own certificate: the transport is per-guard
	// (built lazily on first use), so this touches nothing else.
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport.TLSClientConfig = &tls.Config{RootCAs: roots}

	client := &http.Client{Transport: transport}

	probe := func() (*http.Response, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		if err != nil {
			return nil, err
		}

		return client.Do(req)
	}

	// 1. the first probe lands on the stranded connection and times out.
	resp, err := probe()
	if resp != nil {
		_ = resp.Body.Close()
	}

	require.Error(t, err, "the first probe must time out on the stranded connection")

	// 2. the next probe must answer — on a connection of its own. This is the
	// whole fix: under HTTP/2 it would ride the stranded connection instead and
	// time out again, sixteen times in a row.
	resp, err = probe()
	require.NoError(t, err, "the probe after a stranded one must dial fresh, not inherit the corpse")
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 1, resp.ProtoMajor,
		"checks must negotiate HTTP/1.1: that is the protocol that drops a connection whose request did not complete")

	conns := seen()
	require.Len(t, conns, 2, "the two probes must have arrived on two different connections")
	require.NotEqual(t, conns[0], conns[1], "the second probe must not have reused the stranded connection")
}

// strandedFirstConnServer returns an HTTP/2-capable TLS test server that
// black-holes its FIRST connection (the handler blocks until the client gives
// up) and answers every later one — a peer that lost the state of one
// connection while the path itself is fine — plus a accessor for the client
// addresses that reached it, in order.
func strandedFirstConnServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()

	var mu sync.Mutex
	var conns []string

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		if len(conns) == 0 || conns[len(conns)-1] != request.RemoteAddr {
			conns = append(conns, request.RemoteAddr)
		}
		stranded := len(conns) == 1 && conns[0] == request.RemoteAddr
		mu.Unlock()

		if stranded {
			<-request.Context().Done()

			return
		}

		writer.WriteHeader(http.StatusOK)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()

	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()

		return append([]string(nil), conns...)
	}
}
