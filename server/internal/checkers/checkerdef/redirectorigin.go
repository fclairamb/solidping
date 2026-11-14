package checkerdef

import (
	"net/http"
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

// RedirectHop is the decision ApplyRedirectHop takes for one redirect hop.
type RedirectHop struct {
	// CrossOrigin is true when the hop leaves the original request's origin
	// (see SameRedirectOrigin): the caller strips its secret headers too.
	CrossOrigin bool
	// WithholdBody is true when the cross-origin hop would replay the request
	// body (a 307 or 308): the caller must stop following and return
	// http.ErrUseLastResponse, so the redirect response itself is reported.
	WithholdBody bool
}

// ApplyRedirectHop applies the parts of the redirect hop rule that do not
// depend on the checker, from a CheckRedirect callback (spec 2026-11-14-02):
//
//   - on a cross-origin hop it deletes Referer, which net/http sets to the
//     full previous URL, query string included, so a resolved secret URL
//     parameter would reach the other host;
//   - it reports whether that hop would replay the body: net/http resends it
//     on a 307 or 308 whatever the new host, and a body can hold resolved
//     secrets.
//
// A same-origin hop is left untouched.
func ApplyRedirectHop(first *url.URL, req *http.Request) RedirectHop {
	if SameRedirectOrigin(first, req.URL) {
		return RedirectHop{}
	}

	req.Header.Del("Referer")

	return RedirectHop{CrossOrigin: true, WithholdBody: RedirectReplaysBody(req)}
}

// RedirectReplaysBody reports whether the hop net/http built carries a
// request body. It does only on a 307 or 308 (301/302/303 drop the body), by
// calling the original request's GetBody. An empty body (http.NoBody) does not
// count: there is nothing to leak.
func RedirectReplaysBody(req *http.Request) bool {
	if req.ContentLength > 0 {
		return true
	}

	return req.GetBody != nil && req.Body != nil && req.Body != http.NoBody
}
