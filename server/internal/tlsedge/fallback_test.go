package tlsedge

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pires/go-proxyproto"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

const (
	// localHost is the hostname the splitter under test serves itself.
	localHost = "mine.example.com"
	// foreignHost is served by the next hop, never here.
	foreignHost = "theirs.example.com"
)

// TestPeerIsUpstream pins the address half of the loop guard, including the
// loopback carve-out: on one machine every instance shares 127.0.0.1, so the
// peer address cannot tell a cycle from a local client and the hop-count TTL
// is what protects that topology.
func TestPeerIsUpstream(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		peer     string
		upstream string
		want     bool
	}{
		{
			name: "the return leg of an A to B to A cycle is caught",
			peer: "10.42.0.9:51234", upstream: "10.42.0.9:443", want: true,
		},
		{
			name: "a normal client is not the upstream",
			peer: "203.0.113.7:44821", upstream: "10.42.0.9:443", want: false,
		},
		{
			// The ports never match on the return leg (the source port is
			// ephemeral), so the comparison has to be on the IP.
			name: "the upstream IP matches whatever its source port is",
			peer: "10.42.0.9:1", upstream: "10.42.0.9:8443", want: true,
		},
		{
			name: "IPv6 peers are compared as addresses, not strings",
			peer: "[2001:db8::1]:44821", upstream: "[2001:0db8:0000::1]:443", want: true,
		},
		{
			name: "loopback is excluded: co-located instances share 127.0.0.1",
			peer: "127.0.0.1:51234", upstream: "127.0.0.1:8443", want: false,
		},
		{
			name: "a named upstream is compared literally, never resolved",
			peer: "10.42.0.9:51234", upstream: "dev.internal:443", want: false,
		},
		{
			name: "an unparseable upstream can never trip the guard",
			peer: "10.42.0.9:51234", upstream: "nonsense", want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, peerIsUpstream(tc.peer, tc.upstream))
		})
	}
}

// TestFallbackListenerRoutesByHost is the splitter's contract in one table:
// what this instance serves is terminated here, everything else leaves over
// PROXY v2 with its bytes untouched, and anything unreadable stays local rather
// than being forwarded on a guess.
func TestFallbackListenerRoutesByHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		request     string
		wantForward bool
	}{
		{
			name:    "a host this instance serves is terminated locally",
			request: "GET /page HTTP/1.1\r\nHost: " + localHost + "\r\nConnection: close\r\n\r\n",
		},
		{
			name:        "an unknown host goes to the next hop",
			request:     "GET /page HTTP/1.1\r\nHost: " + foreignHost + "\r\nConnection: close\r\n\r\n",
			wantForward: true,
		},
		{
			// Never forward on a parse failure: the connection may well be ours.
			name:    "an unparseable head stays local",
			request: "\x00\x01\x02 not http\r\n\r\n",
		},
		{
			name:    "a request with no Host stays local",
			request: "GET / HTTP/1.0\r\n\r\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			harness := startSplitter(t, splitterOptions{})

			response, err := harness.roundTrip(t, tc.request)
			r.NoError(err)

			if tc.wantForward {
				r.Contains(response, upstreamMarker, "the next hop must have answered")
				r.Equal(int64(1), harness.listener.stats.forwarded.Load())
				r.Zero(harness.listener.stats.local.Load())

				forwarded := harness.forwarded(t)
				r.Equal(tc.request, forwarded.payload,
					"the next hop must receive the original bytes, byte for byte")

				return
			}

			r.NotContains(response, upstreamMarker, "a local host must never reach the next hop")
			r.Equal(int64(1), harness.listener.stats.local.Load())
			r.Zero(harness.listener.stats.forwarded.Load())
			harness.assertNothingForwarded(t)
		})
	}
}

// TestFallbackListenerForwardsOriginalClient is the PROXY v2 claim: the address
// the next hop sees is the ORIGINAL client, not this hop — including when this
// hop itself learned the client from an inbound PROXY header, which is the
// production topology (Traefik → prod → dev).
func TestFallbackListenerForwardsOriginalClient(t *testing.T) {
	t.Parallel()

	t.Run("the direct peer is forwarded when there is no inbound header", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		harness := startSplitter(t, splitterOptions{})

		_, err := harness.roundTrip(t, "GET / HTTP/1.1\r\nHost: "+foreignHost+"\r\n\r\n")
		r.NoError(err)

		forwarded := harness.forwarded(t)
		r.NotNil(forwarded.header)
		r.Equal(byte(2), forwarded.header.Version)
		r.Equal(harness.lastClientAddr, forwarded.header.SourceAddr.String())
	})

	t.Run("an inbound PROXY header's client is carried on", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		harness := startSplitter(t, splitterOptions{
			acme: &config.ACMEConfig{
				ProxyProtocol:             true,
				ProxyProtocolTrustedCIDRs: []string{loopbackCIDR},
			},
		})

		preamble := proxyHeader(t, 2)

		_, err := harness.roundTripWithPreamble(t,
			preamble, "GET / HTTP/1.1\r\nHost: "+foreignHost+"\r\n\r\n")
		r.NoError(err)

		forwarded := harness.forwarded(t)
		r.NotNil(forwarded.header)
		r.Equal(advertisedClient, forwarded.header.SourceAddr.String(),
			"the next hop must see the real client, not this hop")
	})

	t.Run("the header can be turned off", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		harness := startSplitter(t, splitterOptions{noProxyProtocol: true})

		_, err := harness.roundTrip(t, "GET / HTTP/1.1\r\nHost: "+foreignHost+"\r\n\r\n")
		r.NoError(err)

		forwarded := harness.forwarded(t)
		r.Nil(forwarded.header, "no PROXY preamble may be sent when the option is off")
	})
}

// TestFallbackListenerDialFailureClosesClient pins the fail-closed rail: a host
// we already decided is not ours must never fall back to local termination when
// the next hop is unreachable — that would answer with our own certificate
// error and hide the real fault.
func TestFallbackListenerDialFailureClosesClient(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	harness := startSplitter(t, splitterOptions{upstream: deadAddress(t)})

	response, err := harness.roundTrip(t, "GET / HTTP/1.1\r\nHost: "+foreignHost+"\r\n\r\n")
	r.Empty(response)
	r.Error(err, "the client connection must be closed, not served locally")

	r.Equal(int64(1), harness.listener.stats.dialFailed.Load())
	r.Zero(harness.listener.stats.local.Load(), "a decided-foreign host must never be served here")
}

// TestFallbackListenerLoopGuard proves a chain misconfigured into a cycle fails
// fast instead of ping-ponging until fd exhaustion, and that it says so once
// per offending peer rather than once per connection.
func TestFallbackListenerLoopGuard(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	logs := &countingHandler{message: "tlsedge: refusing to forward, the fallback chain loops back here"}

	harness := startSplitter(t, splitterOptions{
		acme: &config.ACMEConfig{
			ProxyProtocol:             true,
			ProxyProtocolTrustedCIDRs: []string{loopbackCIDR},
		},
		logger: slog.New(logs),
	})

	// A header that says this connection has already crossed the whole chain:
	// exactly what the return leg of a cycle carries.
	preamble := loopedProxyHeader(t, fallbackMaxHops)

	for range 2 {
		_, err := harness.roundTripWithPreamble(t, preamble,
			"GET / HTTP/1.1\r\nHost: "+foreignHost+"\r\n\r\n")
		r.Error(err, "a looping connection must be dropped")
	}

	r.Equal(int64(2), harness.listener.stats.refused.Load())
	r.Zero(harness.listener.stats.forwarded.Load())
	harness.assertNothingForwarded(t)
	r.Equal(int64(1), logs.count.Load(), "a cycle must be reported once per peer, not once per connection")
}

// TestFallbackListenerRecordsPrometheusCounters proves the operator-visible
// half of the observability rail: a chained deployment is otherwise silent
// about where its connections went. The listener label is unique to this test
// so parallel cases cannot perturb the counter.
func TestFallbackListenerRecordsPrometheusCounters(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const label = "test-counters"

	forwardedBefore := testutil.ToFloat64(
		prommetrics.TLSEdgeConnections.WithLabelValues(label, outcomeForwarded))
	localBefore := testutil.ToFloat64(
		prommetrics.TLSEdgeConnections.WithLabelValues(label, outcomeLocal))

	harness := startSplitter(t, splitterOptions{listenerLabel: label})

	_, err := harness.roundTrip(t, "GET / HTTP/1.1\r\nHost: "+foreignHost+"\r\n\r\n")
	r.NoError(err)

	_, err = harness.roundTrip(t, "GET / HTTP/1.1\r\nHost: "+localHost+"\r\nConnection: close\r\n\r\n")
	r.NoError(err)

	r.InDelta(forwardedBefore+1, testutil.ToFloat64(
		prommetrics.TLSEdgeConnections.WithLabelValues(label, outcomeForwarded)), 0.001)
	r.InDelta(localBefore+1, testutil.ToFloat64(
		prommetrics.TLSEdgeConnections.WithLabelValues(label, outcomeLocal)), 0.001)
}

// TestFallbackListenerRequiresAnUpstream is the second lock behind the config
// validation: a splitter with nothing to forward to would silently drop every
// unknown-host connection.
func TestFallbackListenerRequiresAnUpstream(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	r.NoError(err)

	t.Cleanup(func() { _ = listener.Close() })

	_, err = newFallbackListener(t.Context(), listener, fallbackConfig{
		upstream: "  ",
		peek:     peekHTTPHost,
		isLocal:  func(context.Context, string) bool { return false },
	})
	r.ErrorIs(err, ErrNoFallbackUpstream)

	_, err = newFallbackListener(t.Context(), listener, fallbackConfig{
		upstream: "10.0.0.1:443",
		isLocal:  func(context.Context, string) bool { return false },
	})
	r.ErrorIs(err, ErrNoFallbackUpstream, "a splitter without a peek could never decide anything")
}

// TestEdgeInstallsSplitterOnlyWhenConfigured pins the default: with no
// fallback upstream the two listeners are exactly what they were before this
// feature existed.
func TestEdgeInstallsSplitterOnlyWhenConfigured(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		upstreamHTTP  string
		upstreamHTTPS string
		wantHTTPSplit bool
		wantTLSSplit  bool
	}{
		{name: "no upstream splits nothing"},
		{
			name:         "both upstreams split both listeners",
			upstreamHTTP: "127.0.0.1:1", upstreamHTTPS: "127.0.0.1:2",
			wantHTTPSplit: true, wantTLSSplit: true,
		},
		{
			// Forwarding :443 without :80 is a legitimate half-configuration
			// (TLS-ALPN-01 only), and must not accidentally split :80.
			name:          "an https-only upstream leaves the plain listener alone",
			upstreamHTTPS: "127.0.0.1:2",
			wantTLSSplit:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			_, dbSvc := newTestStorage(t)

			edge, err := New(&Options{
				ACME: config.ACMEConfig{
					Enabled:               true,
					Email:                 "ops@solidping.io",
					ListenHTTP:            "127.0.0.1:0",
					ListenHTTPS:           "127.0.0.1:0",
					FallbackUpstreamHTTP:  tc.upstreamHTTP,
					FallbackUpstreamHTTPS: tc.upstreamHTTPS,
				},
				DB:                   dbSvc,
				CustomDomainServable: func(context.Context, string) bool { return false },
				Handler:              http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
			})
			r.NoError(err)
			r.NoError(edge.Start(t.Context()))

			t.Cleanup(func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = edge.Shutdown(shutdownCtx)
			})

			_, httpSplit := edge.httpLn.(*fallbackListener)
			_, httpsSplit := edge.httpsLn.(*fallbackListener)

			r.Equal(tc.wantHTTPSplit, httpSplit, "the ACME HTTP listener")
			r.Equal(tc.wantTLSSplit, httpsSplit, "the HTTPS listener")
		})
	}
}

// --- harness ---------------------------------------------------------------

// upstreamMarker appears in the fake next hop's reply, so a test can tell which
// hop answered from the client side alone.
const upstreamMarker = "X-Answered-By: upstream"

type splitterOptions struct {
	// upstream overrides the fake next hop's address (used for dial failures).
	upstream string
	// acme enables inbound PROXY protocol on the splitter's own listener.
	acme *config.ACMEConfig
	// noProxyProtocol turns the outbound PROXY header off.
	noProxyProtocol bool
	// listenerLabel overrides the metric/log label.
	listenerLabel string
	logger        *slog.Logger
}

// splitterHarness is a fallbackListener with a local HTTP server on top and a
// fake next hop behind it — the smallest thing that exercises a real accept,
// peek, decision and splice over real sockets.
type splitterHarness struct {
	addr     string
	listener *fallbackListener
	received chan forwardedConn

	lastClientAddr string
}

// forwardedConn is what the fake next hop saw.
type forwardedConn struct {
	header  *proxyproto.Header
	payload string
}

func startSplitter(t *testing.T, opts splitterOptions) *splitterHarness {
	t.Helper()
	r := require.New(t)

	harness := &splitterHarness{received: make(chan forwardedConn, 4)}

	upstreamAddr := opts.upstream
	if upstreamAddr == "" {
		upstreamAddr = startFakeUpstream(t, harness.received)
	}

	var lc net.ListenConfig

	raw, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	r.NoError(err)

	harness.addr = raw.Addr().String()

	inner := raw

	if opts.acme != nil {
		inner, err = wrapProxyProtocol(raw, opts.acme)
		r.NoError(err)
	}

	label := opts.listenerLabel
	if label == "" {
		label = listenerHTTP
	}

	logger := opts.logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	split, err := newFallbackListener(t.Context(), inner, fallbackConfig{
		listener:      label,
		upstream:      upstreamAddr,
		proxyProtocol: !opts.noProxyProtocol,
		peek:          peekHTTPHost,
		peekTimeout:   2 * time.Second,
		isLocal: func(_ context.Context, host string) bool {
			return host == localHost
		},
		log: logger,
	})
	r.NoError(err)

	harness.listener = split

	srv := &http.Server{
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
			_, _ = io.WriteString(writer, "local:"+req.Host)
		}),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	go func() { _ = srv.Serve(split) }()

	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	})

	return harness
}

// roundTrip opens one connection, writes a raw request and reads whatever comes
// back until the peer closes.
func (h *splitterHarness) roundTrip(t *testing.T, request string) (string, error) {
	t.Helper()

	return h.roundTripWithPreamble(t, nil, request)
}

func (h *splitterHarness) roundTripWithPreamble(t *testing.T, preamble []byte, request string) (string, error) {
	t.Helper()

	dialer := &net.Dialer{Timeout: 5 * time.Second}

	conn, err := dialer.DialContext(t.Context(), "tcp", h.addr)
	if err != nil {
		return "", err
	}

	defer func() { _ = conn.Close() }()

	h.lastClientAddr = conn.LocalAddr().String()

	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))

	if len(preamble) > 0 {
		if _, writeErr := conn.Write(preamble); writeErr != nil {
			return "", writeErr
		}
	}

	if _, writeErr := io.WriteString(conn, request); writeErr != nil {
		return "", writeErr
	}

	response, err := io.ReadAll(conn)
	if err != nil {
		return string(response), err
	}

	if len(response) == 0 {
		return "", io.EOF
	}

	return string(response), nil
}

// forwarded returns what the fake next hop received, failing the test if
// nothing arrived.
func (h *splitterHarness) forwarded(t *testing.T) forwardedConn {
	t.Helper()

	select {
	case conn := <-h.received:
		return conn
	case <-time.After(5 * time.Second):
		t.Fatal("the next hop never received the connection")

		return forwardedConn{}
	}
}

// assertNothingForwarded fails when anything reached the next hop.
func (h *splitterHarness) assertNothingForwarded(t *testing.T) {
	t.Helper()

	select {
	case conn := <-h.received:
		t.Fatalf("nothing should have reached the next hop, got %q", conn.payload)
	case <-time.After(200 * time.Millisecond):
	}
}

// startFakeUpstream stands in for the downstream instance: it parses the PROXY
// preamble (when there is one), records the request head it received and
// answers with a recognizable marker.
func startFakeUpstream(t *testing.T, received chan<- forwardedConn) string {
	t.Helper()
	r := require.New(t)

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	r.NoError(err)

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			go handleFakeUpstream(conn, received)
		}
	}()

	return listener.Addr().String()
}

func handleFakeUpstream(conn net.Conn, received chan<- forwardedConn) {
	defer func() { _ = conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	reader := bufio.NewReader(conn)

	var header *proxyproto.Header

	// A PROXY preamble is optional here: one of the cases turns it off.
	if peeked, err := reader.Peek(len(proxyproto.SIGV2)); err == nil &&
		bytes.Equal(peeked, proxyproto.SIGV2) {
		header, _ = proxyproto.Read(reader)
	}

	var head bytes.Buffer

	for {
		line, err := reader.ReadString('\n')
		head.WriteString(line)

		if err != nil || line == "\r\n" {
			break
		}
	}

	received <- forwardedConn{header: header, payload: head.String()}

	_, _ = io.WriteString(conn,
		"HTTP/1.1 200 OK\r\n"+upstreamMarker+"\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
}

// loopedProxyHeader builds the preamble the return leg of a cycle carries: a
// normal v2 header plus the hop-count TLV this edge writes when it forwards.
func loopedProxyHeader(t *testing.T, hops int) []byte {
	t.Helper()
	r := require.New(t)

	source, err := net.ResolveTCPAddr("tcp", advertisedClient)
	r.NoError(err)

	destination, err := net.ResolveTCPAddr("tcp", advertisedDest)
	r.NoError(err)

	header := proxyproto.HeaderProxyFromAddrs(2, source, destination)
	r.NoError(header.SetTLVs([]proxyproto.TLV{{Type: fallbackHopTLV, Value: []byte{byte(hops)}}}))

	raw, err := header.Format()
	r.NoError(err)

	return raw
}

// deadAddress returns an address nothing listens on, for the dial-failure case.
func deadAddress(t *testing.T) string {
	t.Helper()
	r := require.New(t)

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	r.NoError(err)

	addr := listener.Addr().String()
	r.NoError(listener.Close())

	return addr
}

// countingHandler counts slog records carrying one specific message, which is
// how the "log once per offending peer" rail is asserted.
type countingHandler struct {
	message string
	count   atomic.Int64
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *countingHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message == h.message {
		h.count.Add(1)
	}

	return nil
}

func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *countingHandler) WithGroup(string) slog.Handler { return h }
