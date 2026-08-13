package tlsedge

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

// The three roles of a chained deployment, as hostnames:
//   - chainUpstreamHost is served by the first hop (Traefik's single catch-all
//     target),
//   - chainDownstreamHost only by the second hop,
//   - chainUnknownHost by nobody, and must end up refused by the last hop.
const (
	chainUpstreamHost   = "upstream-owned.example.com"
	chainDownstreamHost = "downstream-owned.example.com"
	chainUnknownHost    = "nobody-owns-this.example.com"
)

// chainDeadCA points certmagic at a port nothing listens on. Every chained test
// runs with it, so a bug that lets an unknown host reach issuance fails fast
// and locally instead of generating real CA traffic.
const chainDeadCA = "https://127.0.0.1:1/directory"

// TestFallbackChainServesDownstreamCertificate is the end-to-end claim of the
// feature: a TLS connection for a host only the SECOND hop serves is handed on
// unterminated, so the certificate the client ends up validating is the
// downstream's — and the downstream sees the ORIGINAL client, not the hop in
// front of it, across two PROXY v2 rewrites.
func TestFallbackChainServesDownstreamCertificate(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	downstream := startFakeTLSDownstream(t, chainDownstreamHost)
	upstream := startChainEdge(t, chainEdgeOptions{
		reserved:        []string{chainUpstreamHost},
		upstreamHTTPS:   downstream.addr,
		inboundProxying: true,
	})

	state, body, err := chainTLSRequest(t, upstream.httpsAddr, chainDownstreamHost,
		proxyHeader(t, 2))
	r.NoError(err, "the chained handshake must complete")

	r.NotEmpty(state.PeerCertificates)
	r.Contains(state.PeerCertificates[0].DNSNames, chainDownstreamHost,
		"the certificate must come from the hop that owns the domain")

	r.Contains(body, "downstream", "the downstream must have served the request")
	r.Contains(body, advertisedClient,
		"the downstream must see the ORIGINAL client address, not the hop in front of it")

	r.Equal(int64(1), upstream.splitterHTTPS(t).stats.forwarded.Load())
	r.Zero(upstream.splitterHTTPS(t).stats.local.Load())
}

// TestFallbackChainKeepsUpstreamHostsLocal is the other half: a host the FIRST
// hop owns must never leave it. Asserted over the plaintext listener, where the
// answer (the challenge handler's redirect) is unambiguous and no certificate
// is involved.
func TestFallbackChainKeepsUpstreamHostsLocal(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	downstream := startFakeTLSDownstream(t, chainDownstreamHost)
	sink := startFakeHTTPDownstream(t)
	upstream := startChainEdge(t, chainEdgeOptions{
		reserved:      []string{chainUpstreamHost},
		upstreamHTTPS: downstream.addr,
		upstreamHTTP:  sink.addr,
	})

	response, err := chainPlainRequest(t, upstream.httpAddr, chainUpstreamHost, "/anything", nil)
	r.NoError(err)
	r.Contains(response, "308", "the first hop's own host must be answered by its own redirect")
	r.Contains(response, "https://"+chainUpstreamHost+"/anything")

	sink.assertNothing(t)
	r.Equal(int64(1), upstream.splitterHTTP(t).stats.local.Load())
	r.Zero(upstream.splitterHTTP(t).stats.forwarded.Load())
}

// TestFallbackChainHTTP01ReachesDownstream walks the plaintext path a
// downstream HTTP-01 challenge depends on, over a three-hop chain of two real
// edges plus a recording last hop:
//
//   - a request for the SECOND hop's own domain is forwarded by the first and
//     answered by the second's challenge handler — it stops there, it does not
//     keep traveling;
//   - a request for a domain nobody owns keeps going, and the last hop sees the
//     ORIGINAL client through both rewrites.
//
// Without this the downstream instance could never solve HTTP-01 for its own
// domains, which is the whole point of forwarding :80 as well as :443.
func TestFallbackChainHTTP01ReachesDownstream(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sink := startFakeHTTPDownstream(t)

	downstream := startChainEdge(t, chainEdgeOptions{
		servable:        func(host string) bool { return host == chainDownstreamHost },
		upstreamHTTP:    sink.addr,
		inboundProxying: true,
	})

	upstream := startChainEdge(t, chainEdgeOptions{
		reserved:        []string{chainUpstreamHost},
		upstreamHTTP:    downstream.httpAddr,
		inboundProxying: true,
	})

	// --- the downstream's own domain stops at the downstream ---------------
	response, err := chainPlainRequest(t, upstream.httpAddr, chainDownstreamHost,
		"/.well-known/acme-challenge/token", proxyHeader(t, 2))
	r.NoError(err)
	r.Contains(response, "308", "the downstream's challenge handler must have answered")
	r.Contains(response, "https://"+chainDownstreamHost+"/.well-known/acme-challenge/token")

	sink.assertNothing(t)
	r.Equal(int64(1), upstream.splitterHTTP(t).stats.forwarded.Load())
	r.Equal(int64(1), downstream.splitterHTTP(t).stats.local.Load(),
		"the connection must have reached the downstream and stayed there")

	// --- a host nobody owns keeps traveling, client address intact --------
	_, _ = chainPlainRequest(t, upstream.httpAddr, chainUnknownHost, "/",
		proxyHeader(t, 2))

	forwarded := sink.forwarded(t)
	r.NotNil(forwarded.header)
	r.Equal(advertisedClient, forwarded.header.SourceAddr.String(),
		"two chained PROXY v2 rewrites must still name the original client")
	r.Contains(forwarded.payload, "Host: "+chainUnknownHost)
	r.Equal(int64(1), downstream.splitterHTTP(t).stats.forwarded.Load())
}

// TestFallbackChainLastHopRefusesUnknownHost pins the fail-closed end of the
// chain: forwarding moves the decision, it does not weaken it. A host nobody
// owns still dies at the last hop, with no certificate and no CA traffic.
func TestFallbackChainLastHopRefusesUnknownHost(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// The last hop: a real edge with no upstream of its own.
	downstream := startChainEdge(t, chainEdgeOptions{
		servable:        func(host string) bool { return host == chainDownstreamHost },
		inboundProxying: true,
	})

	upstream := startChainEdge(t, chainEdgeOptions{
		reserved:      []string{chainUpstreamHost},
		upstreamHTTPS: downstream.httpsAddr,
	})

	_, _, err := chainTLSRequest(t, upstream.httpsAddr, chainUnknownHost, nil)
	r.Error(err, "a host nobody owns must not get a handshake anywhere in the chain")

	r.Equal(int64(1), upstream.splitterHTTPS(t).stats.forwarded.Load(),
		"the first hop must have handed it on rather than refusing it itself")
	r.ErrorIs(downstream.edge.Decide(t.Context(), chainUnknownHost), ErrHostNotAllowed)
}

// --- chain harness ---------------------------------------------------------

type chainEdgeOptions struct {
	// reserved are the hostnames this hop owns outright.
	reserved []string
	// servable answers the custom-domain gate.
	servable func(host string) bool
	// upstreamHTTPS / upstreamHTTP are this hop's next hops.
	upstreamHTTPS string
	upstreamHTTP  string
	// inboundProxying makes this hop honor a PROXY preamble from loopback.
	inboundProxying bool
}

// chainEdge is one hop under test.
type chainEdge struct {
	edge      *Edge
	httpAddr  string
	httpsAddr string
}

func (c *chainEdge) splitterHTTPS(t *testing.T) *fallbackListener {
	t.Helper()

	split, ok := c.edge.httpsLn.(*fallbackListener)
	require.True(t, ok, "the HTTPS listener must be split")

	return split
}

func (c *chainEdge) splitterHTTP(t *testing.T) *fallbackListener {
	t.Helper()

	split, ok := c.edge.httpLn.(*fallbackListener)
	require.True(t, ok, "the plain listener must be split")

	return split
}

func startChainEdge(t *testing.T, opts chainEdgeOptions) *chainEdge {
	t.Helper()
	r := require.New(t)

	_, dbSvc := newTestStorage(t)

	servable := opts.servable
	if servable == nil {
		servable = func(string) bool { return false }
	}

	acme := config.ACMEConfig{
		Enabled: true,
		Email:   "ops@solidping.io",
		// Nothing listens there: an unexpected issuance attempt fails locally
		// instead of reaching a real CA.
		CAURL:                         chainDeadCA,
		ListenHTTP:                    "127.0.0.1:0",
		ListenHTTPS:                   "127.0.0.1:0",
		FallbackUpstreamHTTP:          opts.upstreamHTTP,
		FallbackUpstreamHTTPS:         opts.upstreamHTTPS,
		FallbackUpstreamProxyProtocol: true,
	}

	if opts.inboundProxying {
		acme.ProxyProtocol = true
		acme.ProxyProtocolTrustedCIDRs = []string{loopbackCIDR}
	}

	edge, err := New(&Options{
		ACME:                 acme,
		DB:                   dbSvc,
		ReservedHosts:        opts.reserved,
		CustomDomainServable: func(_ context.Context, host string) bool { return servable(host) },
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
			_, _ = io.WriteString(writer, "edge-local:"+req.Host)
		}),
		Logger: slog.New(slog.DiscardHandler),
	})
	r.NoError(err)
	r.NoError(edge.Start(t.Context()))

	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = edge.Shutdown(shutdownCtx)
	})

	return &chainEdge{
		edge:      edge,
		httpAddr:  edge.httpLn.Addr().String(),
		httpsAddr: edge.httpsLn.Addr().String(),
	}
}

// fakeTLSDownstream stands in for a second instance that owns a domain and
// holds its certificate: a PROXY-protocol listener terminating TLS with a
// certificate for that domain, answering with what it believes the client to
// be. It is what makes "the downstream served it, with its own certificate,
// for the original client" observable without a CA.
type fakeTLSDownstream struct {
	addr string
}

func startFakeTLSDownstream(t *testing.T, host string) *fakeTLSDownstream {
	t.Helper()
	r := require.New(t)

	var lc net.ListenConfig

	raw, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	r.NoError(err)

	addr := raw.Addr().String()

	wrapped, err := wrapProxyProtocol(raw, &config.ACMEConfig{
		ProxyProtocol:             true,
		ProxyProtocolTrustedCIDRs: []string{loopbackCIDR},
	})
	r.NoError(err)

	srv := &http.Server{
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
			_, _ = fmt.Fprintf(writer, "downstream host=%s client=%s", req.Host, req.RemoteAddr)
		}),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	tlsConfig := testTLSConfigFor(t, host)

	go func() { _ = srv.Serve(tls.NewListener(wrapped, tlsConfig)) }()

	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	})

	return &fakeTLSDownstream{addr: addr}
}

// fakeHTTPDownstream is the plaintext equivalent: it records the PROXY header
// and request head it receives, so a test can prove what reached the last hop.
type fakeHTTPDownstream struct {
	addr     string
	received chan forwardedConn
}

func startFakeHTTPDownstream(t *testing.T) *fakeHTTPDownstream {
	t.Helper()

	sink := &fakeHTTPDownstream{received: make(chan forwardedConn, 4)}
	sink.addr = startFakeUpstream(t, sink.received)

	return sink
}

func (s *fakeHTTPDownstream) forwarded(t *testing.T) forwardedConn {
	t.Helper()

	select {
	case conn := <-s.received:
		return conn
	case <-time.After(5 * time.Second):
		t.Fatal("the last hop never received the connection")

		return forwardedConn{}
	}
}

func (s *fakeHTTPDownstream) assertNothing(t *testing.T) {
	t.Helper()

	select {
	case conn := <-s.received:
		t.Fatalf("nothing should have reached the last hop, got %q", conn.payload)
	case <-time.After(200 * time.Millisecond):
	}
}

// chainTLSRequest dials the first hop, optionally as a trusted proxy would,
// completes a TLS handshake for the given SNI and issues one request. It
// returns the connection state (the certificate the chain ended up presenting)
// and the response body.
func chainTLSRequest(t *testing.T, addr, serverName string, preamble []byte) (tls.ConnectionState, string, error) {
	t.Helper()

	dialer := &net.Dialer{Timeout: 5 * time.Second}

	conn, err := dialer.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		return tls.ConnectionState{}, "", err
	}

	defer func() { _ = conn.Close() }()

	require.NoError(t, conn.SetDeadline(time.Now().Add(20*time.Second)))

	if len(preamble) > 0 {
		if _, writeErr := conn.Write(preamble); writeErr != nil {
			return tls.ConnectionState{}, "", writeErr
		}
	}

	client := tls.Client(conn, &tls.Config{
		ServerName: serverName,
		// The downstream's certificate is self-signed in these tests; the
		// assertion is which hop issued it, not that it chains to a root.
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	})
	if handshakeErr := client.HandshakeContext(t.Context()); handshakeErr != nil {
		return tls.ConnectionState{}, "", handshakeErr
	}

	if _, err = fmt.Fprintf(client,
		"GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", serverName); err != nil {
		return client.ConnectionState(), "", err
	}

	response, err := http.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		return client.ConnectionState(), "", err
	}

	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(response.Body)

	return client.ConnectionState(), string(body), err
}

// chainPlainRequest issues one plaintext request into the chain and returns the
// raw response, which is how the HTTP-01 path is observed.
func chainPlainRequest(t *testing.T, addr, host, path string, preamble []byte) (string, error) {
	t.Helper()

	dialer := &net.Dialer{Timeout: 5 * time.Second}

	conn, err := dialer.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		return "", err
	}

	defer func() { _ = conn.Close() }()

	require.NoError(t, conn.SetDeadline(time.Now().Add(20*time.Second)))

	if len(preamble) > 0 {
		if _, writeErr := conn.Write(preamble); writeErr != nil {
			return "", writeErr
		}
	}

	if _, err = fmt.Fprintf(conn,
		"GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, host); err != nil {
		return "", err
	}

	response, err := io.ReadAll(conn)

	return string(response), err
}

// TestFallbackChainLocalTLSHandshake covers the local half of the HTTPS
// splitter without a CA: a connection for a host this instance DOES serve must
// reach the TLS stack with a byte-for-byte identical ClientHello, or the
// handshake fails. The forwarded path proves the replay towards the next hop;
// this proves the replay into crypto/tls right here.
func TestFallbackChainLocalTLSHandshake(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	downstream := startFakeTLSDownstream(t, chainDownstreamHost)

	var lc net.ListenConfig

	raw, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	r.NoError(err)

	addr := raw.Addr().String()

	split, err := newFallbackListener(t.Context(), raw, fallbackConfig{
		listener:      listenerHTTPS,
		upstream:      downstream.addr,
		proxyProtocol: true,
		peek:          peekTLSClientHello,
		peekTimeout:   2 * time.Second,
		isLocal: func(_ context.Context, host string) bool {
			return host == chainUpstreamHost
		},
		log: slog.New(slog.DiscardHandler),
	})
	r.NoError(err)

	tlsConfig := testTLSConfigFor(t, chainUpstreamHost)

	srv := &http.Server{
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
			_, _ = io.WriteString(writer, "local-tls:"+req.Host)
		}),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	go func() { _ = srv.Serve(tls.NewListener(split, tlsConfig)) }()

	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	})

	state, body, err := chainTLSRequest(t, addr, chainUpstreamHost, nil)
	r.NoError(err, "a replayed ClientHello must still complete the local handshake")
	r.NotEmpty(state.PeerCertificates)
	r.Contains(state.PeerCertificates[0].DNSNames, chainUpstreamHost)
	r.Contains(body, "local-tls:"+chainUpstreamHost)
	r.Equal(int64(1), split.stats.local.Load())
	r.Zero(split.stats.forwarded.Load())
}
