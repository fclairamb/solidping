package freebox

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/fclairamb/solidping/server/internal/egress"
)

// ErrBaseURLInvalid is the sentinel every ValidateBaseURL rejection wraps.
var ErrBaseURLInvalid = errors.New("baseUrl must be a valid Freebox API endpoint")

// allowedPorts are the ports a Freebox API endpoint may listen on: 80/443 are
// the box's own local defaults, 8443 is the documented remote-access HTTPS
// port operators configure under Freebox OS's own Settings > Remote access.
//
//nolint:gochecknoglobals // constant lookup table
var allowedPorts = map[string]bool{"80": true, "443": true, "8443": true}

// ValidateBaseURL enforces the URL contract a Freebox `baseUrl` override must
// satisfy (spec 2026-09-25-31). Talking to the member's own box is the whole
// point of this feature, so a private-range destination is legitimate —
// unlike a check target or a notification webhook, this validator does not
// reject on destination alone. It still closes the gap: an ordinary org
// member should not be able to point the pairing handshake (which POSTs and
// reads results back through the pairing-status endpoint) at an arbitrary
// public host or port.
//
// The empty string always passes (the caller means "use the default"), and so
// does the exact default (DefaultBaseURL) as a fast path. Otherwise:
//
//   - no userinfo;
//   - scheme is http or https;
//   - the host is either an IP literal or a *.freebox.fr name — an arbitrary
//     DNS name is rejected, since it buys nothing a documented Freebox
//     hostname does not already cover and only widens what a pairing request
//     can be pointed at;
//   - a private-range IP (RFC1918, loopback, link-local, …) is the member's
//     own LAN: any scheme and any port is accepted, since a home Freebox can
//     be reconfigured to answer on a nonstandard port and local Freebox APIs
//     are commonly served over plain HTTP;
//   - anything else — a public IP, or a *.freebox.fr hostname (remote
//     access) — is held to the documented remote-access contract: https
//     only, except for the vendor's own mafreebox.freebox.fr name over http,
//     and a port of 80, 443 or 8443.
func ValidateBaseURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == DefaultBaseURL {
		return nil
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBaseURLInvalid, err)
	}

	if parsed.User != nil {
		return fmt.Errorf("%w: must not contain userinfo", ErrBaseURLInvalid)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: scheme must be http or https, got %q", ErrBaseURLInvalid, parsed.Scheme)
	}

	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("%w: missing host", ErrBaseURLInvalid)
	}

	ip := net.ParseIP(host)
	isFreeboxHost := isFreeboxDomain(host)

	if ip == nil && !isFreeboxHost {
		return fmt.Errorf(
			"%w: host must be an IP address or a *.freebox.fr name, got %q", ErrBaseURLInvalid, host)
	}

	if ip != nil && egress.IsNonPublic(ip) {
		// The member's own LAN (or loopback, which is also how test fixtures
		// simulate a local Freebox): no further restriction.
		return nil
	}

	if scheme == "http" && !isFreeboxHost {
		return fmt.Errorf(
			"%w: http is only allowed for a private-range IP or a *.freebox.fr host — use https",
			ErrBaseURLInvalid)
	}

	return validatePort(parsed.Port(), scheme)
}

// validatePort checks port (as parsed.Port() returns it — empty when the URL
// left it implicit) against allowedPorts, defaulting to the scheme's standard
// port first.
func validatePort(port, scheme string) error {
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	if !allowedPorts[port] {
		return fmt.Errorf("%w: port must be 80, 443 or 8443, got %q", ErrBaseURLInvalid, port)
	}

	return nil
}

// isFreeboxDomain reports whether host is freebox.fr or any subdomain of it
// (which covers the documented default, mafreebox.freebox.fr).
func isFreeboxDomain(host string) bool {
	lower := strings.ToLower(host)

	return lower == "freebox.fr" || strings.HasSuffix(lower, ".freebox.fr")
}
