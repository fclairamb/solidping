package auth

import (
	"net/http"
	"slices"
)

// MCP credentials on REST (spec 2026-10-16-12).
//
// An OAuth access token and a scoped PAT are bearer tokens on the same
// verification path as the dashboard session, but their consent is narrower:
// an `mcp:read` grant must not hand the client the user's full REST role, a
// PAT mint or a dashboard session. RequireAuth, the realtime WS handshake,
// feedback attribution and the OAuth consent endpoint all refuse them; the MCP
// resource server (RequireMCPAuth) is the only place they work.
const (
	// CLIOAuthClientID is the client_id of the pre-registered first-party CLI
	// OAuth client (oauth.CLIClientID aliases it). Dynamic registrations get a
	// random server-generated ID, so no third party can claim it.
	CLIOAuthClientID = "solidping-cli"

	// scopeMCPFull is the read-write MCP scope (oauth.ScopeMCP).
	scopeMCPFull = "mcp"

	// MCPCredentialRESTMessage is the 401 detail when an MCP credential is
	// presented to a REST route.
	MCPCredentialRESTMessage = "This token is only valid for the MCP endpoint"
)

// IsMCPCredential reports whether these claims come from a credential whose
// consent is scoped to the MCP resource: an OAuth access token (it carries an
// `aud` and, since spec 2026-10-16-12, the client ID) or a PAT created with
// scopes. Keyed on `aud` too so tokens minted before the client ID claim
// existed are recognized.
func (c *Claims) IsMCPCredential() bool {
	return c != nil && (len(c.Audience) > 0 || len(c.Scopes) > 0 || c.ClientID != "")
}

// mcpCredentialRoute is one REST route an MCP credential may still reach.
type mcpCredentialRoute struct {
	method  string
	pattern string
	// cliOnly restricts the entry to the first-party CLI client holding the
	// full `mcp` scope.
	cliOnly bool
}

// mcpCredentialAllowedRoutes is an ALLOWLIST: an MCP credential reaches no
// REST route but these.
//
//nolint:gochecknoglobals // Effectively a constant table; Go has no const slices.
var mcpCredentialAllowedRoutes = []mcpCredentialRoute{
	// Dropping your own grant is never an escalation (spec 2026-07-18-05).
	{method: http.MethodDelete, pattern: ImpersonationPathTokenCurrent},
	// The legacy loopback CLI login (spec 2026-07-11-08) resolves its org with
	// GET /auth/me, then mints its PAT, both with the OAuth access token right
	// after the code exchange (finishBrowserLogin, removed in e4c9cb79). The
	// in-tree CLI uses the device flow since spec 2026-08-08-02.
	// TODO(remove after v0.40.0): drop these two entries, and the PAT step of
	// TestCLIBrowserLoginWalkthrough, once released CLIs no longer use the
	// loopback flow.
	{method: http.MethodGet, pattern: ImpersonationPathMe, cliOnly: true},
	{method: http.MethodPost, pattern: ImpersonationPatternOrgTokens, cliOnly: true},
}

// IsMCPCredentialRouteAllowed reports whether an MCP credential may perform
// this REST request. routePattern is chi's resolved pattern
// (httpx.RoutePattern), as for the demo and impersonation guards.
func IsMCPCredentialRouteAllowed(claims *Claims, method, routePattern string) bool {
	for i := range mcpCredentialAllowedRoutes {
		route := &mcpCredentialAllowedRoutes[i]
		if route.method != method || route.pattern != routePattern {
			continue
		}

		if !route.cliOnly {
			return true
		}

		return claims != nil && claims.ClientID == CLIOAuthClientID &&
			slices.Contains(claims.Scopes, scopeMCPFull)
	}

	return false
}

// MCPCredentialAllowedRoutes returns the allowlist as (method, pattern) pairs,
// for the route-table test in internal/app that proves every entry names a
// route that really exists.
func MCPCredentialAllowedRoutes() [][2]string {
	out := make([][2]string, 0, len(mcpCredentialAllowedRoutes))
	for i := range mcpCredentialAllowedRoutes {
		out = append(out, [2]string{mcpCredentialAllowedRoutes[i].method, mcpCredentialAllowedRoutes[i].pattern})
	}

	return out
}
