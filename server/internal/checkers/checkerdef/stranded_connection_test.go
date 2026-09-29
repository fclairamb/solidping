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
	// from every entry point.
	for _, ctx := range []context.Context{
		context.Background(),
		egress.WithGuard(context.Background(), egress.New(true)),
	} {
		shared, ok := HTTPTransportFor(ctx, false).(*http.Transport)
		require.True(t, ok, "an untunneled check must get an *http.Transport")
		require.Same(t, checkTransport, shared,
			"an untunneled check must share the pooled HTTP/1.1 transport")
		require.Same(t, checkTransport, GuardedHTTPClient(ctx).Transport,
			"the DefaultClient replacement must share it too")
	}

	built, ok := BuildHTTPTransport(nil, false, IPVersionAuto).(*http.Transport)
	require.True(t, ok, "BuildHTTPTransport must get an *http.Transport")
	require.Same(t, checkTransport, built,
		"BuildHTTPTransport must not fall back to http.DefaultTransport")
	require.Same(t, checkTransport, CheckHTTPTransport())

	// Enforcing guard (every SaaS worker) → the guard's own pooled transport,
	// which must be HTTP/1.1 for exactly the same reason.
	enforcing := egress.New(false)
	enforcingCtx := egress.WithGuard(context.Background(), enforcing)
	transport, ok := HTTPTransportFor(enforcingCtx, false).(*http.Transport)
	require.True(t, ok, "the enforcing path must hand out the guard's *http.Transport")
	require.Same(t, enforcing.HTTPTransport(), transport)
	require.Same(t, transport, GuardedHTTPClient(enforcingCtx).Transport)
	require.False(t, transport.ForceAttemptHTTP2,
		"the guard's shared transport must not attempt HTTP/2 either")

	for _, rt := range []http.RoundTripper{checkTransport, transport} {
		require.NotSame(t, http.DefaultTransport, rt)
	}
}

// TestCheckTransportsNegotiateHTTP1 is the behavioral half of the contract
// above: against a server that offers h2 in ALPN, every transport a check
// probe can be handed — shared or private — ends up speaking HTTP/1.1. The
// field assertions above say how; this says it actually happens on the wire.
func TestCheckTransportsNegotiateHTTP1(t *testing.T) {
	t.Parallel()

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	enforcing := egress.WithGuard(context.Background(),
		egress.New(false, egress.WithPublicOverride(net.ParseIP("127.0.0.1"))))
	tunnel := DialerFunc((&net.Dialer{}).DialContext)

	cases := map[string]http.RoundTripper{
		"shared, self-hosted":      HTTPTransportFor(context.Background(), false),
		"shared, enforcing guard":  HTTPTransportFor(enforcing, false),
		"guarded client, self":     GuardedHTTPClient(context.Background()).Transport,
		"guarded client, enforced": GuardedHTTPClient(enforcing).Transport,
		"private, verifySsl false": BuildHTTPTransport(nil, true, IPVersionAuto),
		"private, pinned ipv4":     BuildHTTPTransport(nil, false, IPVersionIPv4),
		"private, tunneled":        BuildHTTPTransport(tunnel, false, IPVersionAuto),
		"private, pinned+enforced": buildHTTPTransport(nil, false, IPVersionIPv4, egress.FromContext(enforcing)),
	}

	for name, rt := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client := &http.Client{Transport: trustingClone(t, rt, server)}

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
			require.NoError(t, err)

			resp, err := client.Do(req)
			require.NoError(t, err)

			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, 1, resp.ProtoMajor, "a check probe must never negotiate HTTP/2")
		})
	}

	// Positive control: the server really does offer HTTP/2, so the HTTP/1.1
	// above is the transports' doing and not the server's.
	control := &http.Client{Transport: trustingClone(t, http.DefaultTransport, server)}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	require.NoError(t, err)

	resp, err := control.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, 2, resp.ProtoMajor, "http.DefaultTransport negotiates HTTP/2 against this server")
}

// TestStrandedConnectionDoesNotSurviveTheProbeThatHitIt is the regression test
// for the 2026-09-28 production failure: a peer that loses connection state
// mid-blip stops answering on that connection while the path itself is fine.
// Every probe that then rides the same connection burns its whole execution
// budget without putting a packet on the wire, and the failure only ends when
// the kernel gives up on the 5-tuple (`tcp_retries2`, ~15 min).
//
// Measured on a production worker: a ~1-minute IPv6 blackhole cost 16
// consecutive failing probes (255 failing minutes in a day, of which only 37
// were the blip itself) and 16 region-heartbeat incidents.
//
// The server strands its FIRST connection and answers every later one; the
// second probe must therefore answer, on a connection of its own. Both shared
// transports are covered: the guard's (every SaaS worker) and the check
// transport (self-hosted, no enforcing guard). With HTTP/2 on either — as
// before the fix — the second probe times out too.
func TestStrandedConnectionDoesNotSurviveTheProbeThatHitIt(t *testing.T) {
	t.Parallel()

	contexts := map[string]context.Context{
		// An enforcing guard with loopback declared public for this test only.
		"enforcing guard": egress.WithGuard(context.Background(),
			egress.New(false, egress.WithPublicOverride(net.ParseIP("127.0.0.1")))),
		"no enforcing guard": context.Background(),
	}

	for name, ctx := range contexts {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			server, seen := strandedFirstConnServer(t)
			defer server.Close()

			shared := HTTPTransportFor(ctx, false)
			if shared == nil {
				// net/http's own fallback for a nil Transport — what the plain
				// path handed checks before this fix.
				shared = http.DefaultTransport
			}

			client := &http.Client{Transport: trustingClone(t, shared, server)}

			probe := func(ctx context.Context) (*http.Response, error) {
				probeCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
				defer cancel()

				req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, server.URL, nil)
				if err != nil {
					return nil, err
				}

				return client.Do(req)
			}

			// 1. the first probe lands on the stranded connection and times out.
			resp, err := probe(ctx)
			if resp != nil {
				_ = resp.Body.Close()
			}

			require.Error(t, err, "the first probe must time out on the stranded connection")

			// 2. the next probe must answer — on a connection of its own. This
			// is the whole fix: under HTTP/2 it would ride the stranded
			// connection instead and time out again, sixteen times in a row.
			resp, err = probe(ctx)
			require.NoError(t, err, "the probe after a stranded one must dial fresh, not inherit the corpse")

			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, 1, resp.ProtoMajor,
				"checks must negotiate HTTP/1.1: the protocol that drops a connection whose request did not complete")

			conns := seen()
			require.Len(t, conns, 2, "the two probes must have arrived on two different connections")
			require.NotEqual(t, conns[0], conns[1], "the second probe must not have reused the stranded connection")
		})
	}
}

// trustingClone clones a check transport and makes the clone trust the test
// server's certificate, leaving the shared original untouched. Cloning keeps
// the protocol configuration (ForceAttemptHTTP2, TLSNextProto), which is what
// these tests are about.
func trustingClone(t *testing.T, rt http.RoundTripper, server *httptest.Server) *http.Transport {
	t.Helper()

	transport, ok := rt.(*http.Transport)
	require.True(t, ok, "a check transport must be an *http.Transport")

	clone := transport.Clone()

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())

	if clone.TLSClientConfig == nil {
		clone.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	clone.TLSClientConfig.RootCAs = roots

	return clone
}

// strandedFirstConnServer returns an HTTP/2-capable TLS test server that
// black-holes its FIRST connection (the handler blocks until the client gives
// up) and answers every later one — a peer that lost the state of one
// connection while the path itself is fine — plus an accessor for the client
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
