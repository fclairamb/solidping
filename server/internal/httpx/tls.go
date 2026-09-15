package httpx

import (
	"net/http"
	"strings"
)

// IsTLS reports whether the request reached us over HTTPS, honoring the
// X-Forwarded-Proto header set by the edge proxy that terminates TLS for
// custom domains.
//
// It exists so every cookie writer in the codebase decides `Secure` the same
// way. `Secure` is deliberately dynamic rather than hard-coded to true: a
// Secure cookie on a plain-HTTP self-hosted or dev instance is silently
// dropped by the browser, and the feature that depends on the cookie (the
// status-page unlock, the embedded MCP OAuth consent flow) would appear to do
// nothing at all. Code scanning flags the dynamic value; that is the
// documented trade-off, not an oversight.
func IsTLS(req *http.Request) bool {
	if req == nil {
		return false
	}

	if req.TLS != nil {
		return true
	}

	return strings.EqualFold(req.Header.Get("X-Forwarded-Proto"), "https")
}
