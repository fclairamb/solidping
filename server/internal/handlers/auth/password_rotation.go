package auth

import (
	"net/http"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// PasswordRotationMessage is the human half of every PASSWORD_CHANGE_REQUIRED
// response. It names the action and the endpoint, so a CLI user or a curl
// scripter reading only the JSON body knows what to do next — a bare
// "Forbidden" would leave them guessing which permission they lack.
const PasswordRotationMessage = "A password change is required before this account can be used. " +
	"Rotate it with POST /api/v1/auth/change-password (or the dashboard's " +
	"\"set a new password\" screen), then retry."

// PasswordRotationRequired reports whether the authenticated principal must
// rotate its password before it may do anything else.
//
// It is the single predicate every surface consults — the REST middleware
// (RequireAuth), the MCP resource server (RequireMCPAuth) and the realtime
// WebSocket handshake — so the rule cannot drift between them the way org
// authorization did before it was funneled through AuthorizeOrgAccess.
//
// What it deliberately does NOT look at is who the user is. The seeded bootstrap admin is
// blocked because its row carries the flag, not because it is the seeded admin;
// an operator-forced reset on any ordinary user is blocked identically.
func PasswordRotationRequired(user *models.User) bool {
	return user != nil && user.MustChangePassword
}

// Paths a must-change-password session may still reach. Exported because the
// route table in internal/app/server.go and the clients that route on the
// refusal all name the same three endpoints.
const (
	// PathChangePassword is the authenticated rotation endpoint.
	PathChangePassword = "/api/v1/auth/change-password"
	// PathMe is how a client discovers that it must rotate.
	PathMe = "/api/v1/auth/me"
	// PathLogout keeps a blocked session from being a session with no exit.
	PathLogout = "/api/v1/auth/logout"
)

// rotationExemptRoute is one method+path pair a must-change-password session may
// still reach.
type rotationExemptRoute struct {
	method string
	path   string
}

// rotationExemptRoutes is the complete allowlist, kept deliberately small and
// written out rather than derived from a prefix, so widening it is an explicit
// edit that shows up in a diff.
//
// Each entry earns its place:
//
//   - change-password is the rotation itself — the one action that clears the
//     flag. Blocking it would make the account permanently unusable.
//   - /auth/me is how a client DISCOVERS it must rotate (it carries
//     mustChangePassword). Without it, a dashboard restoring a stored session
//     has no non-failing call to learn its own state from.
//   - /auth/logout, because trapping a user in a session they cannot leave is
//     its own failure mode, and logging out grants nothing.
//
// Everything else — reading checks, minting a personal access token, opening
// the realtime socket — is denied.
//
//nolint:gochecknoglobals // Effectively a constant table; Go has no const slices.
var rotationExemptRoutes = []rotationExemptRoute{
	{http.MethodPost, PathChangePassword},
	{http.MethodGet, PathMe},
	{http.MethodPost, PathLogout},
}

// IsPasswordRotationExempt reports whether the request may proceed despite the
// caller's pending rotation.
//
// The match is on the exact method and path — no prefix matching, so
// "/api/v1/auth/me/anything" is not exempt — with a trailing slash tolerated
// because chi treats "/auth/me" and "/auth/me/" as the same route. OPTIONS is
// exempt unconditionally: a CORS preflight carries no credentials and
// answering it reveals nothing.
func IsPasswordRotationExempt(method, path string) bool {
	if method == http.MethodOptions {
		return true
	}

	normalized := path
	if len(normalized) > 1 {
		normalized = strings.TrimSuffix(normalized, "/")
	}

	for i := range rotationExemptRoutes {
		if rotationExemptRoutes[i].method == method && rotationExemptRoutes[i].path == normalized {
			return true
		}
	}

	return false
}
