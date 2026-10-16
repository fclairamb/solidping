package realtimews_test

import (
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// TestServe_MCPCredentialRejectedWith401: an MCP OAuth access token is valid on
// the MCP endpoint only (spec 2026-10-16-12), so the live socket refuses it
// before the upgrade, exactly as RequireAuth does on REST.
func TestServe_MCPCredentialRejectedWith401(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	fx := newWSFixture(t, wsFixtureOpts{})
	fx.seedOrgAndUser(t)

	member, err := fx.dbSvc.GetUserByEmail(ctx, "member@example.com")
	r.NoError(err)

	mcpToken, err := fx.authService.GenerateMCPAccessToken(ctx, member.UID, "test", []string{"mcp:read"},
		"https://solidping.test/api/v1/mcp", time.Hour, "", "dyn-123")
	r.NoError(err)

	fx.dialExpect401(t, &websocket.DialOptions{Subprotocols: []string{"bearer." + mcpToken, "solidping.v2"}})

	// Positive control: a dashboard session opens the socket.
	conn, err := fx.dialOrg(t, "test", fx.token(t))
	r.NoError(err)
	t.Cleanup(func() { _ = conn.CloseNow() })
	r.Equal("hello", readJSON[helloFrame](t, conn).Type)
}
