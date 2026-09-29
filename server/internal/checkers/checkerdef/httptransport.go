package checkerdef

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/fclairamb/solidping/server/internal/egress"
)

// familyDialTimeout / familyDialKeepAlive mirror http.DefaultTransport's dialer
// settings, so pinning the address family changes only the family — not the
// connect timeout or the keep-alive behavior a check has always had.
const (
	familyDialTimeout   = 30 * time.Second
	familyDialKeepAlive = 30 * time.Second
)

// BuildHTTPTransport returns the http.Transport an HTTP-speaking check needs,
// or the shared check transport when it needs nothing special.
//
// Not returning nil matters in the other direction: net/http would then use
// http.DefaultTransport, which speaks HTTP/2 — and HTTP/2 is exactly what a
// check must not use (see checkTransport below). The shared checkTransport
// keeps the connection pool across executions without that hazard.
//
// It lives in checkerdef (rather than in checkhttp, where it was born) so every
// checker that speaks plain HTTP — checkhttp, checkprometheus — honors
// `tunnelCheckUid` and `ipVersion` through exactly the same code path instead of
// each growing its own near-copy.
func BuildHTTPTransport(
	dialer ContextDialer, skipTLSVerify bool, version IPVersion,
) http.RoundTripper {
	return buildHTTPTransport(dialer, skipTLSVerify, version, nil)
}

// HTTPTransportFor is BuildHTTPTransport for one execution: it reads the
// tunnel dialer, the pinned address family AND the egress guard off ctx. Every
// HTTP-speaking checker goes through it, which is what makes the egress policy
// a single choke point rather than a per-checker convention.
//
// With nothing to customize an untunneled check shares a pooled transport —
// the guard's under an enforcing policy, checkTransport otherwise — so
// connection reuse survives; anything to customize (tunnel, `ipVersion` pin,
// verifySsl: false) gets a private transport for this execution only, dialing
// through the guard when it enforces.
func HTTPTransportFor(ctx context.Context, skipTLSVerify bool) http.RoundTripper {
	return buildHTTPTransport(TunnelDialerFrom(ctx), skipTLSVerify, IPVersionFrom(ctx), egress.FromContext(ctx))
}

// checkTransport is the transport HTTP checks dial through when nothing else
// customizes the dial: no tunnel, no pinned family, no verifySsl: false and no
// enforcing egress guard (the self-hosted posture).
//
// It is deliberately NOT http.DefaultTransport, and it deliberately does NOT
// speak HTTP/2. HTTP/2's handling of a canceled request is what turns one
// network blip into an outage: when a probe's context expires, the client
// cancels the STREAM, and — if it has read no frames since sending the request —
// Go keeps that stream "consuming a concurrency slot until we can confirm the
// server is still responding" (`net/http/internal/http2`,
// clientStream.cleanupWriteRequest). The connection therefore goes back into
// the pool holding a stream that never completes, `CloseIdleConnections`
// refuses to touch it (`len(cc.streams) > 0`), and every later probe rides the
// same dead connection until the kernel gives up on the 5-tuple
// (`tcp_retries2`, ~15 min). Measured on a production worker: a ~1-minute IPv6
// blackhole cost 16 consecutive failing probes, 86 % of that check's failures
// over a day (spec 2026-09-28-04).
//
// HTTP/1.1 has the opposite guarantee: net/http closes the connection of any
// request that did not complete normally, so a connection can never outlive the
// probe that stranded it — while pooling across executions (and across a
// redirect chain inside one execution) is kept. Every private transport below
// was already HTTP/1.1 (a custom DialContext or TLSClientConfig makes net/http
// "conservatively disable HTTP/2"); this makes the two shared ones match.
var checkTransport = &http.Transport{ //nolint:gochecknoglobals // shared, constant after init
	// HTTP/2 off — see the doc comment. An explicit empty TLSNextProto says
	// "no alternate protocols", which stops net/http re-enabling h2 on top of
	// this flag.
	TLSNextProto:      map[string]func(authority string, c *tls.Conn) http.RoundTripper{},
	ForceAttemptHTTP2: false,
	// Dialer settings mirror http.DefaultTransport so a check that used to run
	// on it keeps the same connect timeout and keep-alive probing.
	DialContext: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	Proxy:                 http.ProxyFromEnvironment,
	MaxIdleConns:          100,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

func buildHTTPTransport(
	dialer ContextDialer, skipTLSVerify bool, version IPVersion, guard *egress.Guard,
) http.RoundTripper {
	pinFamily := dialer == nil && version.Explicit()
	enforce := dialer == nil && guard.Enforcing()

	if dialer == nil && !skipTLSVerify && !pinFamily {
		if enforce {
			return guard.HTTPTransport()
		}

		return checkTransport
	}

	transport := &http.Transport{}

	switch {
	case dialer != nil:
		transport.DialContext = dialer.DialContext
	case pinFamily || enforce:
		transport.DialContext = guardedFamilyDialContext(version, guard)
	}

	if skipTLSVerify {
		// InsecureSkipVerify is only set when the operator explicitly
		// opted out of verification via verifySsl: false.
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	return transport
}

// FamilyDialContext returns a DialContext pinned to one address family.
//
// It resolves and selects explicitly (rather than just handing "tcp6" to
// net.Dialer) so that a target with no address of the requested family fails
// with checkerdef's cataloged error naming the host and the family, instead of
// the stdlib's opaque "no suitable address found" — and so the worker-has-no-v6
// case is told apart from the target-has-no-AAAA case. The error travels out
// through *url.Error, which unwraps, so errors.Is still matches at the top.
func FamilyDialContext(version IPVersion) func(context.Context, string, string) (net.Conn, error) {
	return guardedFamilyDialContext(version, nil)
}

// guardedFamilyDialContext is FamilyDialContext under an egress guard. With
// the family auto, the guard does the whole job (resolve once, refuse the
// non-public answers, dial the pinned IP). With a pinned family the family
// selection stays checkerdef's, and the selected IP is checked before it is
// dialed — still the pinned address, never the name again.
func guardedFamilyDialContext(
	version IPVersion, guard *egress.Guard,
) func(context.Context, string, string) (net.Conn, error) {
	baseDialer := &net.Dialer{Timeout: familyDialTimeout, KeepAlive: familyDialKeepAlive}
	network := version.Network("tcp")

	if !version.Explicit() {
		return func(ctx context.Context, dialNetwork, addr string) (net.Conn, error) {
			return guard.DialContextWith(ctx, baseDialer, dialNetwork, addr)
		}
	}

	return func(ctx context.Context, _, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid address %q: %w", addr, err)
		}

		if ip := net.ParseIP(host); ip != nil {
			if !MatchesIPVersion(ip, version) {
				return nil, fmt.Errorf(
					"%w: %s is not an %s address", ErrNoAddressForFamily, host, version.Label(),
				)
			}

			if denyErr := guard.CheckAddr(ctx, host, ip); denyErr != nil {
				return nil, denyErr
			}

			return baseDialer.DialContext(ctx, network, addr)
		}

		addrs, err := LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve hostname: %w", err)
		}

		ip, err := SelectIPAddr(host, addrs, version)
		if err != nil {
			return nil, err
		}

		if denyErr := guard.CheckAddr(ctx, host, ip); denyErr != nil {
			return nil, denyErr
		}

		return baseDialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
}
