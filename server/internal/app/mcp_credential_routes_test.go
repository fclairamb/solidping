package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

// TestMCPCredentialAllowlistNamesRealRoutes keeps the MCP-credential REST
// allowlist (spec 2026-10-16-12) honest: every entry must be a registered
// route, or a renamed endpoint would silently stop working for MCP clients.
func TestMCPCredentialAllowlistNamesRealRoutes(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newImpersonationEnv(t, true)

	registered := map[string]bool{}
	r.NoError(env.server.router.Walk(func(method, pattern string) error {
		registered[method+" "+pattern] = true

		return nil
	}))

	allowlist := auth.MCPCredentialAllowedRoutes()
	r.NotEmpty(allowlist)

	for _, entry := range allowlist {
		r.Truef(registered[entry[0]+" "+entry[1]],
			"MCP credential allowlist entry %s %s is not a registered route", entry[0], entry[1])
	}
}
