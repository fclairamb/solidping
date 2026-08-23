package realtimews_test

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

// TestServe_ForcedPasswordRotationDenied covers the one authenticated surface
// that does NOT go through RequireAuth (spec 2026-08-23-04).
//
// This handler validates the credential in-band, because a browser cannot send
// a bearer at WebSocket-upgrade time — so the central gate has to be repeated
// here, and without a test the live socket would be a silent hole in it: a
// flagged account could still stream every event in the org.
//
// The denial happens PRE-upgrade (a real HTTP 401 with the machine-readable
// code), which is why this asserts on the dial error rather than on a close
// code, unlike the org-access denials in this package.
func TestServe_ForcedPasswordRotationDenied(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	fx := newWSFixture(t, wsFixtureOpts{})
	fx.seedOrgAndUser(t)

	// Log in BEFORE flagging: the session is legitimately issued, exactly as it
	// would be for a user an operator forces a rotation on mid-session.
	resp, err := fx.authService.Login(ctx, "test", "member@example.com", "pw", auth.Context{})
	r.NoError(err)

	conn, err := fx.dialOrg(t, "test", resp.AccessToken)
	r.NoError(err, "precondition: this session opens the socket while unflagged")
	r.Equal("hello", readJSON[helloFrame](t, conn).Type)
	_ = conn.CloseNow()

	member, err := fx.dbSvc.GetUserByEmail(ctx, "member@example.com")
	r.NoError(err)

	flagged := true
	r.NoError(fx.dbSvc.UpdateUser(ctx, member.UID, &models.UserUpdate{MustChangePassword: &flagged}))

	_, err = fx.dialOrg(t, "test", resp.AccessToken)
	r.Error(err, "a flagged account must not reach the live socket")
	r.Contains(err.Error(), strconv.Itoa(http.StatusUnauthorized),
		"the refusal happens before the upgrade, as a readable HTTP status")
}
