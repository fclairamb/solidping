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
// or nil when it needs none.
//
// Returning nil matters: with a nil Transport net/http uses the shared
// http.DefaultTransport, which keeps its connection pool across executions.
// Only a check that actually needs a tunnel dialer, a relaxed TLS config, or a
// pinned address family pays for a private transport.
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
// Under an enforcing guard an untunneled check never gets a nil (default)
// transport: with nothing else to customize it shares the guard's pooled
// transport, so connection reuse survives; otherwise its private transport
// dials through the guard.
func HTTPTransportFor(ctx context.Context, skipTLSVerify bool) http.RoundTripper {
	return buildHTTPTransport(TunnelDialerFrom(ctx), skipTLSVerify, IPVersionFrom(ctx), egress.FromContext(ctx))
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

		return nil
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

			if err := guard.CheckAddr(ctx, host, ip); err != nil {
				return nil, err
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

		if err := guard.CheckAddr(ctx, host, ip); err != nil {
			return nil, err
		}

		return baseDialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
}
