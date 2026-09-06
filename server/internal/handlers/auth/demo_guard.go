package auth

import (
	"net/http"
)

// DemoWriteMessage is the human half of every DEMO_READ_ONLY response. It says
// what the session IS rather than what it lacks, and names the one thing it can
// still do, so a curl user or a CLI reading only the JSON body understands why
// a perfectly valid credential was refused (a bare "Forbidden" would read as a
// permission bug).
const DemoWriteMessage = "This is the shared read-only live demo. " +
	"Creating and editing your own checks is allowed; everything else is not. " +
	"Sign up for a free account to make changes."

// Route patterns a demo session may write to. These are chi's RESOLVED route
// patterns (httpx.RoutePattern), never raw request paths: matching on the raw
// path would let a previous-slug redirect, a trailing slash or a crafted check
// identifier smuggle a request past the list.
const (
	// DemoPathLogout keeps a demo session from being a session with no exit.
	DemoPathLogout = "/api/v1/auth/logout"
	// DemoPatternChecks is check creation — the whole point of the demo.
	DemoPatternChecks = "/api/v1/orgs/{org}/checks"
	// DemoPatternChecksValidate is the dry-run validation the check form calls
	// as you type. It writes nothing.
	DemoPatternChecksValidate = "/api/v1/orgs/{org}/checks/validate"
	// DemoPatternCheck is edit/delete of ONE check. The guard lets the method
	// through; whether this particular check may be touched is an OWNERSHIP
	// question, answered in checks.Service against checks.created_by.
	DemoPatternCheck = "/api/v1/orgs/{org}/checks/{checkUid}"
	// DemoPatternCheckClone clones a check into one the visitor owns — which
	// is how a seeded, un-owned check becomes editable without ever being
	// mutated.
	DemoPatternCheckClone = "/api/v1/orgs/{org}/checks/{checkUid}/clone"
)

// demoAllowedRoute is one method+route-pattern pair a demo session may write.
type demoAllowedRoute struct {
	method  string
	pattern string
}

// demoAllowedRoutes is the COMPLETE allowlist. It is an allowlist and not a
// denylist on purpose: a forgotten endpoint then fails closed, and every future
// mutating route — a new integration type, a new org setting, whatever the next
// spec adds — is refused for demo sessions without anybody having to remember
// this file exists.
//
// Each entry earns its place:
//
//   - /auth/logout, because trapping a visitor in a session they cannot leave
//     is its own failure mode, and logging out grants nothing.
//   - POST /checks, because creating a check and watching results arrive from
//     three continents inside a minute IS the demo. Load is bounded elsewhere,
//     by the org's MaxChecks and MaxChecksPerMinute entitlements.
//   - POST /checks/validate, the dry-run the create form calls as you type. It
//     persists nothing; without it the form cannot give feedback.
//   - PATCH / DELETE /checks/{checkUid} and POST /checks/{checkUid}/clone,
//     bounded by ownership in the service layer.
//
// Deliberately NOT here: PUT /checks/{slug} (upsert can overwrite a seeded
// check by slug), /checks/import, /checks/apply, /checks/export,
// /checks/{checkUid}/rotate-token, and every non-check route in the API.
//
// The check *diagnostics* routes are mentioned in the spec as candidates
// because they are read-only probes — but every one of them is registered as a
// GET, so they already pass on the method rule and need no entry.
//
//nolint:gochecknoglobals // Effectively a constant table; Go has no const slices.
var demoAllowedRoutes = []demoAllowedRoute{
	{http.MethodPost, DemoPathLogout},
	{http.MethodPost, DemoPatternChecks},
	{http.MethodPost, DemoPatternChecksValidate},
	{http.MethodPatch, DemoPatternCheck},
	{http.MethodDelete, DemoPatternCheck},
	{http.MethodPost, DemoPatternCheckClone},
}

// IsDemoWriteAllowed reports whether a demo session may perform this request.
//
// routePattern is the router's RESOLVED pattern (httpx.RoutePattern), not the
// request path. An empty pattern — no chi route context, which in practice
// means the request never matched a registered route — is refused: the guard
// fails closed by construction, which is the entire point of an allowlist.
//
// Safe methods pass unconditionally. GET/HEAD are the read-only demo; OPTIONS
// is a CORS preflight that carries no credentials and mutates nothing.
func IsDemoWriteAllowed(method, routePattern string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}

	if routePattern == "" {
		return false
	}

	for i := range demoAllowedRoutes {
		if demoAllowedRoutes[i].method == method && demoAllowedRoutes[i].pattern == routePattern {
			return true
		}
	}

	return false
}

// DemoAllowedRoutes returns the allowlist as (method, pattern) pairs. Exported
// for the route-table test in internal/app, which walks every registered
// non-GET route and asserts that everything outside this set is refused — that
// is what makes the allowlist a structural property rather than a promise.
func DemoAllowedRoutes() [][2]string {
	out := make([][2]string, 0, len(demoAllowedRoutes))
	for i := range demoAllowedRoutes {
		out = append(out, [2]string{demoAllowedRoutes[i].method, demoAllowedRoutes[i].pattern})
	}

	return out
}
