package checkerdef

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"github.com/fclairamb/solidping/server/internal/egress"
)

// probeTLSHandshakeTimeout mirrors the shared check transport's.
const probeTLSHandshakeTimeout = 10 * time.Second

// HTTPProbeTransportFor is HTTPTransportFor with a required HTTP version
// (spec 2026-10-03-01). It returns the transport to run ONE probe on and the
// function that releases it once the probe is done (always safe to call).
//
//   - HTTPVersion11 is exactly HTTPTransportFor: the shared pooled transport
//     in the common case, a private one when something customizes the dial.
//   - HTTPVersion2 is a fresh HTTP/2-only transport (h2 over TLS, h2c with
//     prior knowledge over http://), keep-alives off, closed by release.
//   - HTTPVersion3 is a fresh HTTP/3 transport over QUIC, closed by release.
//     No TCP fallback: an unreachable UDP path is a failed probe.
//
// Every variant dials through the same tunnel / family / egress-guard rules.
func HTTPProbeTransportFor(
	ctx context.Context, skipTLSVerify bool, httpVersion HTTPVersion,
) (http.RoundTripper, func()) {
	return buildProbeTransport(
		TunnelDialerFrom(ctx), skipTLSVerify, IPVersionFrom(ctx), egress.FromContext(ctx), httpVersion,
	)
}

func buildProbeTransport(
	dialer ContextDialer, skipTLSVerify bool, version IPVersion, guard *egress.Guard, httpVersion HTTPVersion,
) (http.RoundTripper, func()) {
	switch httpVersion.Normalized() {
	case HTTPVersion2:
		transport := buildHTTP2Transport(dialer, skipTLSVerify, version, guard)

		return transport, transport.CloseIdleConnections
	case HTTPVersion3:
		if dialer != nil {
			return errorRoundTripper{err: ErrHTTP3OverTunnel}, func() {}
		}

		transport := buildHTTP3Transport(skipTLSVerify, version, guard)

		return transport, func() { _ = transport.Close() }
	default:
		return buildHTTPTransport(dialer, skipTLSVerify, version, guard), func() {}
	}
}

// buildHTTP2Transport returns an HTTP/2-only transport for one probe. It is
// never shared and never keeps a connection alive past its request, so the
// stranded-stream failure of spec 2026-09-28-04 cannot carry over to the next
// probe: the connection dies with the transport.
func buildHTTP2Transport(
	dialer ContextDialer, skipTLSVerify bool, version IPVersion, guard *egress.Guard,
) *http.Transport {
	protocols := new(http.Protocols)
	protocols.SetHTTP2(true)
	// http:// targets: h2c with prior knowledge.
	protocols.SetUnencryptedHTTP2(true)

	return &http.Transport{
		Protocols:           protocols,
		DisableKeepAlives:   true,
		DialContext:         probeDialContext(dialer, version, guard),
		TLSClientConfig:     probeTLSConfig(skipTLSVerify),
		TLSHandshakeTimeout: probeTLSHandshakeTimeout,
	}
}

// buildHTTP3Transport returns an HTTP/3 transport for one probe. Its dial hook
// applies the TCP path's rules: the pinned family is honored and, under an
// enforcing egress guard, the name is resolved once, a non-public address is
// refused, and the pinned IP is dialed (never the name again).
func buildHTTP3Transport(skipTLSVerify bool, version IPVersion, guard *egress.Guard) *http3.Transport {
	return &http3.Transport{
		TLSClientConfig: probeTLSConfig(skipTLSVerify),
		Dial:            quicDialFunc(version, guard),
	}
}

func quicDialFunc(
	version IPVersion, guard *egress.Guard,
) func(context.Context, string, *tls.Config, *quic.Config) (*quic.Conn, error) {
	return func(ctx context.Context, addr string, tlsCfg *tls.Config, cfg *quic.Config) (*quic.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid address %q: %w", addr, err)
		}

		ip, err := resolveGuarded(ctx, host, version, guard)
		if err != nil {
			return nil, err
		}

		target := net.JoinHostPort(ip.String(), port)

		trace := httptrace.ContextClientTrace(ctx)
		if trace != nil && trace.ConnectStart != nil {
			trace.ConnectStart("udp", target)
		}

		conn, err := quic.DialAddrEarly(ctx, target, tlsCfg, cfg)

		if trace != nil && trace.ConnectDone != nil {
			trace.ConnectDone("udp", target, err)
		}

		if err != nil {
			return nil, fmt.Errorf("HTTP/3 (QUIC) dial %s: %w", target, err)
		}

		return conn, nil
	}
}

// errorRoundTripper fails every request with err, without any network I/O.
type errorRoundTripper struct {
	err error
}

func (e errorRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, e.err
}
