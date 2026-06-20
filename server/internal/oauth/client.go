package oauth

import (
	"net"
	"net/url"
	"strings"
)

// IsLoopbackRedirectURI reports whether a redirect URI is a permitted native
// loopback redirect (RFC 8252 §7.3): an http URL whose host is the IPv4 or IPv6
// loopback address or the literal "localhost", on any port. Native MCP clients
// (Claude Desktop, mcp-remote) spin up an ephemeral loopback listener and cannot
// pre-register an exact port, so the port is intentionally not matched.
//
// Everything else over http is rejected — plaintext http to a non-loopback host
// would leak the authorization code in transit.
func IsLoopbackRedirectURI(redirectURI string) bool {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return false
	}

	if u.Scheme != "http" {
		return false
	}

	host := u.Hostname()
	if host == "localhost" {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

// IsValidRedirectURI reports whether a redirect URI is acceptable to register or
// to use at /authorize when no exact allow-list match is required:
//   - loopback http (native clients), or
//   - https with a host (registered web clients).
//
// A bare http non-loopback URI, a missing host, or a fragment (forbidden by
// RFC 6749 §3.1.2) is rejected.
func IsValidRedirectURI(redirectURI string) bool {
	u, err := url.Parse(redirectURI)
	if err != nil || u.Host == "" || u.Fragment != "" {
		return false
	}

	if IsLoopbackRedirectURI(redirectURI) {
		return true
	}

	return u.Scheme == "https"
}

// RedirectURIAllowed reports whether requestedURI is permitted for a client whose
// registered redirect URIs are registered. Registered web URIs require an exact
// string match (no substring / prefix matching — that is a classic open-redirect
// bug). Loopback URIs match a registered loopback entry ignoring only the port,
// since the native client's port is ephemeral.
func RedirectURIAllowed(requestedURI string, registered []string) bool {
	if requestedURI == "" {
		return false
	}

	for _, reg := range registered {
		if reg == requestedURI {
			return true
		}

		// Loopback: match host+path, ignore the ephemeral port.
		if IsLoopbackRedirectURI(reg) && IsLoopbackRedirectURI(requestedURI) &&
			loopbackHostPathEqual(reg, requestedURI) {
			return true
		}
	}

	return false
}

// loopbackHostPathEqual compares two loopback redirect URIs ignoring the port.
func loopbackHostPathEqual(a, b string) bool {
	ua, err := url.Parse(a)
	if err != nil {
		return false
	}

	ub, err := url.Parse(b)
	if err != nil {
		return false
	}

	return ua.Hostname() == ub.Hostname() && ua.Path == ub.Path
}

// ScopesValid reports whether every requested scope is a recognized MCP scope.
// An empty request defaults to the full "mcp" scope at the call site, so this
// only validates explicitly-requested scopes.
func ScopesValid(scopes []string) bool {
	for _, s := range scopes {
		if s != ScopeMCP && s != ScopeMCPRead {
			return false
		}
	}

	return true
}

// ParseScopes splits a space-delimited scope string (RFC 6749 §3.3) into a slice,
// dropping empty entries.
func ParseScopes(scope string) []string {
	fields := strings.Fields(scope)

	return fields
}
