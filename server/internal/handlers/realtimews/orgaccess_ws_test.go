package realtimews_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/realtimews"
)

// dialOrg opens a WS to a specific org's endpoint (the fixture's wsURL is baked
// to "test"), authenticated with an explicit bearer header.
func (fx *wsFixture) dialOrg(t *testing.T, org, token string) (*websocket.Conn, error) {
	t.Helper()
	url := "ws" + strings.TrimPrefix(fx.server.URL, "http") + "/api/v1/orgs/" + org + "/events/ws"
	conn, resp, err := websocket.Dial(t.Context(), url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}},
	})
	if resp != nil && resp.Body != nil {
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	if conn != nil {
		t.Cleanup(func() { _ = conn.CloseNow() })
	}

	return conn, err
}

// TestServe_TwoOrgMemberMatchesREST is the WS half of the cross-tenant fix
// (spec 2026-07-15-01). A genuine non-super-admin member of BOTH orgs gets a
// live socket for whichever org its token is scoped to, and a 4403 for the
// other — matching, by construction (both call auth.AuthorizeOrgAccess), what
// the REST middleware does for the same tokens. This is the WS surface of the
// (token-org × target-org) agreement the leak violated.
func TestServe_TwoOrgMemberMatchesREST(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	fx := newWSFixture(t, wsFixtureOpts{})
	fx.seedOrgAndUser(t) // org "test" + member@example.com (member of test)

	orgDefault := models.NewOrganization("default", "Default Org")
	r.NoError(fx.dbSvc.CreateOrganization(ctx, orgDefault))
	// Make the same user a member of "default" too (non-super-admin, two orgs).
	member, err := fx.dbSvc.GetUserByEmail(ctx, "member@example.com")
	r.NoError(err)
	r.NoError(fx.dbSvc.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(orgDefault.UID, member.UID, models.MemberRoleUser)))

	// Token scoped to "test".
	testResp, err := fx.authService.Login(ctx, "test", "member@example.com", "pw", auth.Context{})
	r.NoError(err)
	// Token scoped to "default".
	defaultResp, err := fx.authService.Login(ctx, "default", "member@example.com", "pw", auth.Context{})
	r.NoError(err)

	expectHello := func(t *testing.T, org, token string) {
		t.Helper()
		rr := require.New(t)
		conn, err := fx.dialOrg(t, org, token)
		rr.NoError(err)
		frame := readJSON[helloFrame](t, conn)
		rr.Equal("hello", frame.Type)
	}
	expectForbidden := func(t *testing.T, org, token string) {
		t.Helper()
		rr := require.New(t)
		conn, err := fx.dialOrg(t, org, token)
		rr.NoError(err)
		readCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		_, _, readErr := conn.Read(readCtx)
		rr.Error(readErr)
		rr.Equal(realtimews.CloseForbidden, websocket.CloseStatus(readErr))
	}

	t.Run("test-token reaches test", func(t *testing.T) {
		t.Parallel()
		expectHello(t, "test", testResp.AccessToken)
	})
	t.Run("test-token denied on default", func(t *testing.T) {
		t.Parallel()
		expectForbidden(t, "default", testResp.AccessToken)
	})
	t.Run("default-token reaches default", func(t *testing.T) {
		t.Parallel()
		expectHello(t, "default", defaultResp.AccessToken)
	})
	t.Run("default-token denied on test", func(t *testing.T) {
		t.Parallel()
		expectForbidden(t, "test", defaultResp.AccessToken)
	})
}

// TestServe_OrgNotFoundCloses4410 covers the split close code: an org that does
// not exist is terminal (4410), distinct from an org-denied 4403 the client
// refreshes-and-retries. Uses a super-admin so the handshake reaches the org
// load rather than short-circuiting on the token-scope check.
func TestServe_OrgNotFoundCloses4410(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	fx := newWSFixture(t, wsFixtureOpts{})

	// The token is minted against a REAL org (FK-safe); the socket then dials a
	// DIFFERENT slug that doesn't exist. A super-admin token crosses orgs, so
	// the handshake reaches the org load and 4410s on the missing "ghost".
	realOrg := models.NewOrganization("real", "Real Org")
	r.NoError(fx.dbSvc.CreateOrganization(ctx, realOrg))

	super := models.NewUser("super@example.com")
	pwd := "$plaintext$pw"
	super.PasswordHash = &pwd
	super.SuperAdmin = true
	r.NoError(fx.dbSvc.CreateUser(ctx, super))

	resp, err := fx.authService.GenerateTokensForOAuth(ctx, super, realOrg, auth.RoleSuperAdmin, "", auth.Context{})
	r.NoError(err)

	conn, err := fx.dialOrg(t, "ghost", resp.AccessToken)
	r.NoError(err)

	readCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, _, readErr := conn.Read(readCtx)
	r.Error(readErr)
	r.Equal(realtimews.CloseNotFound, websocket.CloseStatus(readErr))
}
