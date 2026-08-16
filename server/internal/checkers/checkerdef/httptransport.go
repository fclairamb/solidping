package checkerdef

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"
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
	pinFamily := dialer == nil && version.Explicit()

	if dialer == nil && !skipTLSVerify && !pinFamily {
		return nil
	}

	transport := &http.Transport{}

	switch {
	case dialer != nil:
		transport.DialContext = dialer.DialContext
	case pinFamily:
		transport.DialContext = FamilyDialContext(version)
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
	baseDialer := &net.Dialer{Timeout: familyDialTimeout, KeepAlive: familyDialKeepAlive}
	network := version.Network("tcp")

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

		return baseDialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
}
