package tlsedge

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/pires/go-proxyproto"

	"github.com/fclairamb/solidping/server/internal/config"
)

// ErrProxyProtocolNoTrustedCIDRs is returned when PROXY protocol support is
// switched on without any trusted source. config.validateACMEProxyProtocol
// already rejects that at startup; this is the second lock, so a listener can
// never be built in a trust-everyone state even if it is wired up directly.
var ErrProxyProtocolNoTrustedCIDRs = errors.New(
	"tlsedge: acme.proxy_protocol requires at least one trusted CIDR",
)

// wrapProxyProtocol wraps a listener so a PROXY protocol (v1 or v2) preamble is
// consumed before the payload — before the TLS handshake on the HTTPS listener,
// so certmagic and crypto/tls sit unchanged on top of it. net/http derives
// Request.RemoteAddr from conn.RemoteAddr(), which the wrapper rewrites to the
// address the proxy advertised, so per-IP rate limiting and request logging see
// the real client with no middleware change.
//
// It returns the listener untouched when acme.proxy_protocol is off, which is
// the default.
//
// The trust policy is the security-relevant part:
//
//   - a source inside a trusted CIDR gets USE — its header is honored when
//     present, and a bare connection is still accepted (kubelet probes and
//     in-cluster health checks do not send one, so REQUIRE would break health
//     checking);
//   - every other source gets IGNORE — the connection proceeds, but its header
//     never overrides RemoteAddr. Without this, anyone able to open a TCP
//     connection could forge a client IP into the rate limiter.
//
// A source whose address cannot be classified at all is dropped by
// Listener.Accept, which keeps listening.
func wrapProxyProtocol(listener net.Listener, cfg *config.ACMEConfig) (net.Listener, error) {
	if !cfg.ProxyProtocol {
		return listener, nil
	}

	trusted := trustedSources(cfg.ProxyProtocolTrustedCIDRs)
	if len(trusted) == 0 {
		return nil, ErrProxyProtocolNoTrustedCIDRs
	}

	policy, err := proxyproto.PolicyFromRanges(trusted, proxyproto.USE, proxyproto.IGNORE)
	if err != nil {
		return nil, fmt.Errorf("tlsedge: invalid acme.proxy_protocol_trusted_cidrs: %w", err)
	}

	return &proxyproto.Listener{
		Listener:   listener,
		ConnPolicy: policy,
		// The preamble read is bounded exactly like the HTTP header read above
		// it: without this, a trusted-looking peer could hold a connection open
		// forever below the layer http.Server's ReadHeaderTimeout protects.
		ReadHeaderTimeout: readHeaderTimeout,
	}, nil
}

// trustedSources trims and drops empty entries so a trailing comma in the env
// value cannot smuggle in a "" range.
func trustedSources(entries []string) []string {
	out := make([]string, 0, len(entries))

	for _, entry := range entries {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			out = append(out, trimmed)
		}
	}

	return out
}
