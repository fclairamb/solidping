package checkerdef

import (
	"net/url"
	"strings"
)

const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

// SameRedirectOrigin reports whether a redirect hop from first to next stays on
// the origin the secret headers were meant for, so they may follow it.
//
// The hostname must be equal (case-insensitive), and then either:
//   - the scheme is unchanged and the effective port (explicit, or the scheme's
//     default) is equal; or
//   - the hop is an http -> https upgrade (the secrets only gain protection).
//
// Any other scheme change, https -> http in particular, is cross-origin: the
// next hop would carry the secrets in cleartext (spec 2026-11-13-04).
func SameRedirectOrigin(first, next *url.URL) bool {
	if !strings.EqualFold(first.Hostname(), next.Hostname()) {
		return false
	}

	firstScheme := strings.ToLower(first.Scheme)
	nextScheme := strings.ToLower(next.Scheme)

	if firstScheme == nextScheme {
		return effectivePort(first) == effectivePort(next)
	}

	return firstScheme == schemeHTTP && nextScheme == schemeHTTPS
}

// effectivePort returns the URL's port, defaulting from its scheme.
func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}

	if strings.EqualFold(u.Scheme, schemeHTTPS) {
		return "443"
	}

	return "80"
}
