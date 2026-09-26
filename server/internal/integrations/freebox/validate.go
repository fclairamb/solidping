package freebox

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// ErrBaseURLInvalid is the sentinel every ValidateBaseURL rejection wraps.
var ErrBaseURLInvalid = errors.New("baseUrl must be a valid Freebox API endpoint")

// allowedPorts are the ports a Freebox API endpoint may listen on: 80/443 are
// the box's own local defaults, 8443 is the documented remote-access HTTPS
// port operators configure under Freebox OS's own Settings > Remote access.
// Enforced for every host — private or not: a "the member's own box" excuse
// does not extend to an arbitrary port, which is exactly the internal
// scan/POST primitive this validator exists to close.
//
//nolint:gochecknoglobals // constant lookup table
var allowedPorts = map[string]bool{"80": true, "443": true, "8443": true}

// freeboxPrivatePrefixes are the ranges a Freebox can actually live at on a
// member's own LAN: RFC 1918 IPv4 and IPv6 ULA. Deliberately narrower than
// egress.IsNonPublic — that list also folds in loopback, link-local
// (including the 169.254.169.254 cloud metadata address), the unspecified
// address and multicast, none of which are legitimate Freebox addresses.
// Treating any of those as "private" would keep exactly the internal
// scan/POST primitive this validator exists to close, so this check is
// deliberately its own, narrower list rather than a reuse of egress's.
//
//nolint:gochecknoglobals // immutable lookup table
var freeboxPrivatePrefixes = mustPrefixes(
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"fc00::/7",
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, cidr := range cidrs {
		out = append(out, netip.MustParsePrefix(cidr))
	}

	return out
}

// isFreeboxPrivateIP reports whether ip is an address a member's own Freebox
// could plausibly sit at: RFC 1918 IPv4 or IPv6 ULA. Loopback, link-local
// (incl. cloud metadata), the unspecified address, multicast, CGNAT and every
// other special range are deliberately NOT included — a Freebox does not live
// there, and accepting them would let an ordinary org member point pairing at
// exactly those targets on any port.
func isFreeboxPrivateIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}

	if addr.Is4In6() {
		addr = addr.Unmap()
	}

	for i := range freeboxPrivatePrefixes {
		if freeboxPrivatePrefixes[i].Contains(addr) {
			return true
		}
	}

	return false
}

// ValidateBaseURL enforces the URL contract a Freebox `baseUrl` override must
// satisfy (spec 2026-09-25-31). Talking to the member's own box is the whole
// point of this feature, so a private-range destination is legitimate —
// unlike a check target or a notification webhook, this validator does not
// reject on destination alone. It still closes the gap: an ordinary org
// member should not be able to point the pairing handshake (which POSTs and
// reads results back through the pairing-status endpoint) at an internal
// service — the cloud metadata address, loopback, or any other special-range
// address that is not actually where a Freebox lives.
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
//   - http is only accepted when the host is a private-range IP
//     (isFreeboxPrivateIP) or a *.freebox.fr name — local Freebox APIs are
//     commonly served over plain HTTP, but a public IP or an arbitrary
//     hostname must use https;
//   - the port (explicit, or the scheme default when omitted) must be 80,
//     443 or 8443 — for every host, private-range IPs included.
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

	privateIP := ip != nil && isFreeboxPrivateIP(ip)

	if scheme == "http" && !privateIP && !isFreeboxHost {
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
