package tlsedge

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pires/go-proxyproto"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

// The client the proxy claims to be forwarding for. It is deliberately a public
// address that this test process could never actually dial from, so "the
// handler saw it" can only mean the PROXY header was honored.
const (
	advertisedClient = "203.0.113.7:44821"
	advertisedDest   = "198.51.100.1:443"
	// loopbackCIDR is the trusted range in the "proxy is trusted" cases: the
	// test's own connections come from 127.0.0.1.
	loopbackCIDR = "127.0.0.0/8"
	// foreignCIDR trusts a range this process cannot connect from, which is how
	// the untrusted-source cases are built.
	foreignCIDR = "10.0.0.0/8"
	// testSNI is the name the TLS client asks for; the test server answers with
	// a self-signed certificate for it.
	testSNI = "proxy.tlsedge.test"
)

// TestProxyProtocolRemoteAddr is the end-to-end claim of the feature: what the
// HTTP handler sees as Request.RemoteAddr — the value the rate limiter and the
// abuse logs are keyed on — under every trust/header combination, over BOTH the
// plain listener and the TLS listener (where the preamble has to be consumed
// before the handshake).
func TestProxyProtocolRemoteAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// trusted is acme.proxy_protocol_trusted_cidrs.
		trusted []string
		// version is the PROXY protocol version to send; 0 means "send no
		// header at all".
		version byte
		// wantAdvertised is true when the handler must observe the address the
		// header advertises, false when it must observe the real peer.
		wantAdvertised bool
	}{
		{
			name:           "trusted source with a v2 header is honored",
			trusted:        []string{loopbackCIDR},
			version:        2,
			wantAdvertised: true,
		},
		{
			name:           "trusted source with a v1 header is honored",
			trusted:        []string{loopbackCIDR},
			version:        1,
			wantAdvertised: true,
		},
		{
			// Kubelet probes and in-cluster health checks never send a
			// preamble: the policy is USE, not REQUIRE, so a bare connection
			// from a trusted source must still be served.
			name:           "trusted source without a header keeps its real address",
			trusted:        []string{loopbackCIDR},
			version:        0,
			wantAdvertised: false,
		},
		{
			// The security case: anyone can open a TCP connection and prepend a
			// forged preamble. From outside the trusted set it must never
			// override the address the rate limiter sees.
			name:           "untrusted source cannot forge an address with v2",
			trusted:        []string{foreignCIDR},
			version:        2,
			wantAdvertised: false,
		},
		{
			name:           "untrusted source cannot forge an address with v1",
			trusted:        []string{foreignCIDR},
			version:        1,
			wantAdvertised: false,
		},
		{
			name:           "untrusted source without a header keeps its real address",
			trusted:        []string{foreignCIDR},
			version:        0,
			wantAdvertised: false,
		},
	}

	for _, tc := range tests {
		for _, transport := range []struct {
			name   string
			useTLS bool
		}{{name: "plain", useTLS: false}, {name: "tls", useTLS: true}} {
			t.Run(tc.name+"/"+transport.name, func(t *testing.T) {
				t.Parallel()
				r := require.New(t)

				addr := startProxyEcho(t, &config.ACMEConfig{
					ProxyProtocol:             true,
					ProxyProtocolTrustedCIDRs: tc.trusted,
				}, transport.useTLS)

				var preamble []byte
				if tc.version != 0 {
					preamble = proxyHeader(t, tc.version, advertisedClient, advertisedDest)
				}

				observed, localAddr, err := requestRemoteAddr(t, addr, preamble, transport.useTLS)
				r.NoError(err)

				if tc.wantAdvertised {
					r.Equal(advertisedClient, observed,
						"the handler must see the client the PROXY header advertises, port included")

					return
				}

				r.Equal(localAddr, observed, "the handler must see the real peer address")
				r.NotContains(observed, "203.0.113.7", "a forged address must never reach the handler")
			})
		}
	}
}

// TestProxyProtocolMalformedHeaderIsIsolated proves a bad preamble from a
// trusted source kills only its own connection: the listener must keep
// accepting, otherwise one malformed packet from the proxy would take the whole
// edge down.
func TestProxyProtocolMalformedHeaderIsIsolated(t *testing.T) {
	t.Parallel()

	for _, transport := range []struct {
		name   string
		useTLS bool
	}{{name: "plain", useTLS: false}, {name: "tls", useTLS: true}} {
		t.Run(transport.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			addr := startProxyEcho(t, &config.ACMEConfig{
				ProxyProtocol:             true,
				ProxyProtocolTrustedCIDRs: []string{loopbackCIDR},
			}, transport.useTLS)

			// A v1 signature with a truncated address list: recognizable as a
			// PROXY header, unparseable as one.
			_, _, err := requestRemoteAddr(t, addr, []byte("PROXY TCP4 192.0.2.1\r\n"), transport.useTLS)
			r.Error(err, "a malformed preamble must fail its own connection")

			// ...and the listener is still serving.
			observed, _, err := requestRemoteAddr(t,
				addr, proxyHeader(t, 2, advertisedClient, advertisedDest), transport.useTLS)
			r.NoError(err, "the listener must keep serving after a malformed preamble")
			r.Equal(advertisedClient, observed)
		})
	}
}

// TestProxyProtocolDisabledLeavesListenerUntouched pins the default: with
// acme.proxy_protocol off, nothing is wrapped and a stray preamble is just
// payload (which the HTTP server rejects) rather than a trusted address source.
func TestProxyProtocolDisabledLeavesListenerUntouched(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	r.NoError(err)

	t.Cleanup(func() { _ = listener.Close() })

	wrapped, err := wrapProxyProtocol(listener, &config.ACMEConfig{})
	r.NoError(err)
	r.Equal(listener, wrapped, "a disabled edge must serve the raw listener")

	// Belt and braces: an enabled edge with no trusted source can never be
	// built, so there is no trust-everyone state to fall into.
	_, err = wrapProxyProtocol(listener, &config.ACMEConfig{ProxyProtocol: true})
	r.ErrorIs(err, ErrProxyProtocolNoTrustedCIDRs)

	_, err = wrapProxyProtocol(listener, &config.ACMEConfig{
		ProxyProtocol:             true,
		ProxyProtocolTrustedCIDRs: []string{"  ", ""},
	})
	r.ErrorIs(err, ErrProxyProtocolNoTrustedCIDRs)

	_, err = wrapProxyProtocol(listener, &config.ACMEConfig{
		ProxyProtocol:             true,
		ProxyProtocolTrustedCIDRs: []string{"not-a-cidr"},
	})
	r.Error(err, "an unparseable trusted range must fail the listener, not be silently dropped")
}

// TestEdgeWrapsBothListeners guards the "both" in "wrap both listeners": the
// plain :80 listener carries the HTTP-01 challenge and the redirect, and its
// client IP feeds the same rate limiter as the TLS one, so leaving it unwrapped
// would half-fix the problem.
func TestEdgeWrapsBothListeners(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		proxy       bool
		wantWrapped bool
	}{
		{name: "proxy protocol on wraps both", proxy: true, wantWrapped: true},
		{name: "proxy protocol off wraps neither", proxy: false, wantWrapped: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			_, dbSvc := newTestStorage(t)

			edge, err := New(&Options{
				ACME: config.ACMEConfig{
					Enabled:                   true,
					Email:                     "ops@solidping.io",
					ListenHTTP:                "127.0.0.1:0",
					ListenHTTPS:               "127.0.0.1:0",
					ProxyProtocol:             tc.proxy,
					ProxyProtocolTrustedCIDRs: []string{loopbackCIDR},
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

			_, httpWrapped := edge.httpLn.(*proxyproto.Listener)
			_, httpsWrapped := edge.httpsLn.(*proxyproto.Listener)

			r.Equal(tc.wantWrapped, httpWrapped, "the ACME HTTP listener")
			r.Equal(tc.wantWrapped, httpsWrapped, "the HTTPS listener")
		})
	}
}

// TestEdgeStartRejectsProxyProtocolWithoutTrustedCIDRs proves the fail-closed
// wiring survives a caller that skipped config validation.
func TestEdgeStartRejectsProxyProtocolWithoutTrustedCIDRs(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, dbSvc := newTestStorage(t)

	edge, err := New(&Options{
		ACME: config.ACMEConfig{
			Enabled: true, Email: "ops@solidping.io",
			ListenHTTP: "127.0.0.1:0", ListenHTTPS: "127.0.0.1:0",
			ProxyProtocol: true,
		},
		DB:                   dbSvc,
		CustomDomainServable: func(context.Context, string) bool { return false },
	})
	r.NoError(err)
	r.ErrorIs(edge.Start(t.Context()), ErrProxyProtocolNoTrustedCIDRs)
}

// startProxyEcho binds an ephemeral loopback port, wraps it exactly as the edge
// does, optionally puts TLS on top (which is the interesting case: the preamble
// must be consumed before the handshake) and serves a handler that echoes
// Request.RemoteAddr. It returns the address to dial.
func startProxyEcho(t *testing.T, acme *config.ACMEConfig, useTLS bool) string {
	t.Helper()
	r := require.New(t)

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	r.NoError(err)

	addr := listener.Addr().String()

	wrapped, err := wrapProxyProtocol(listener, acme)
	r.NoError(err)

	if useTLS {
		wrapped = tls.NewListener(wrapped, testTLSConfig(t))
	}

	srv := &http.Server{
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
			_, _ = io.WriteString(writer, req.RemoteAddr)
		}),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	go func() { _ = srv.Serve(wrapped) }()

	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	})

	return addr
}

// requestRemoteAddr opens one connection, optionally writes a raw preamble,
// then speaks HTTP (over TLS when asked) and returns what the handler saw
// alongside this connection's real local address — which is what the handler
// must report whenever the header is not honored.
func requestRemoteAddr(t *testing.T, addr string, preamble []byte, useTLS bool) (string, string, error) {
	t.Helper()

	dialer := &net.Dialer{Timeout: 5 * time.Second}

	conn, err := dialer.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		return "", "", err
	}

	defer func() { _ = conn.Close() }()

	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))

	localAddr := conn.LocalAddr().String()

	if len(preamble) > 0 {
		if _, writeErr := conn.Write(preamble); writeErr != nil {
			return "", localAddr, writeErr
		}
	}

	var stream net.Conn

	stream = conn

	if useTLS {
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName: testSNI,
			// The test server presents a self-signed certificate generated in
			// process; the assertion under test is the observed RemoteAddr.
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		})
		if handshakeErr := tlsConn.HandshakeContext(t.Context()); handshakeErr != nil {
			return "", localAddr, handshakeErr
		}

		stream = tlsConn
	}

	if _, err = fmt.Fprintf(stream,
		"GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", testSNI); err != nil {
		return "", localAddr, err
	}

	resp, err := http.ReadResponse(bufio.NewReader(stream), nil)
	if err != nil {
		return "", localAddr, err
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", localAddr, err
	}

	return strings.TrimSpace(string(body)), localAddr, nil
}

// proxyHeader formats a real PROXY protocol header (v1 or v2) for the given
// source/destination, using the same library a proxy would.
func proxyHeader(t *testing.T, version byte, source, destination string) []byte {
	t.Helper()
	r := require.New(t)

	sourceAddr, err := net.ResolveTCPAddr("tcp", source)
	r.NoError(err)

	destinationAddr, err := net.ResolveTCPAddr("tcp", destination)
	r.NoError(err)

	header := &proxyproto.Header{
		Version:           version,
		Command:           proxyproto.PROXY,
		TransportProtocol: proxyproto.TCPv4,
		SourceAddr:        sourceAddr,
		DestinationAddr:   destinationAddr,
	}

	raw, err := header.Format()
	r.NoError(err)

	return raw
}

// testTLSConfig serves a self-signed certificate for testSNI, standing in for
// certmagic's on-demand certificate: the point of the TLS cases is where the
// PROXY preamble is consumed relative to the handshake, not how the leaf was
// obtained.
func testTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	r := require.New(t)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	r.NoError(err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: testSNI},
		DNSNames:     []string{testSNI},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	r.NoError(err)

	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}
}
