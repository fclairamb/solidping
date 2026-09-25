package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// TestSwitchOrgAcceptsOrgLessSession pins the half of spec 2026-09-25-15 the
// dashboard relies on: an org-less session (no org in the token, no refresh
// token) that belongs to an org re-mints for it through switch-org, through the
// real RequireAuth chain. Without it, an org-less session could never leave
// /no-org for an org the user IS a member of.
//
// The negative is paired: the same token asking for an org the user does not
// belong to gets the endpoint's existing anti-enumeration 401.
func TestSwitchOrgAcceptsOrgLessSession(t *testing.T) {
	t.Parallel()

	f := setupRotationFixture(t)
	handler := f.mw.RequireAuth(auth.NewHandler(f.authSvc, nil).SwitchOrg)

	const (
		email    = "alice@acme.com"
		password = "correct-horse-battery"
	)

	hash, err := passwords.Hash(password)
	require.NoError(t, err)

	user := models.NewUser(email)
	user.PasswordHash = &hash
	require.NoError(t, f.dbSvc.CreateUser(f.ctx, user))

	// Signed in while belonging to no org: an org-less session.
	login, err := f.authSvc.Login(f.ctx, "", email, password, auth.Context{})
	require.NoError(t, err)
	require.Empty(t, login.RefreshToken, "precondition: an org-less login has no refresh token")
	require.Nil(t, login.Organization, "precondition: the session is org-less")

	claims, err := f.authSvc.ValidateToken(f.ctx, login.AccessToken)
	require.NoError(t, err)
	require.Empty(t, claims.OrgSlug)

	// The membership exists by the time the session wants to use it.
	own := models.NewOrganization("acmetech", "Acme Tech")
	require.NoError(t, f.dbSvc.CreateOrganization(f.ctx, own))
	require.NoError(t, f.dbSvc.CreateOrganizationMember(f.ctx,
		models.NewOrganizationMember(own.UID, user.UID, models.MemberRoleAdmin)))

	foreign := models.NewOrganization("demo", "Demo")
	require.NoError(t, f.dbSvc.CreateOrganization(f.ctx, foreign))

	call := func(t *testing.T, org string) *httptest.ResponseRecorder {
		t.Helper()

		req := httptest.NewRequestWithContext(f.ctx, http.MethodPost, "/api/v1/auth/switch-org",
			strings.NewReader(`{"org":"`+org+`"}`))
		req.Header.Set("Authorization", "Bearer "+login.AccessToken)
		req.Header.Set("Content-Type", "application/json")

		rec := httptest.NewRecorder()
		require.NoError(t, handler(rec, req))

		return rec
	}

	t.Run("member org answers 200 with a full session", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		rec := call(t, "acmetech")
		r.Equal(http.StatusOK, rec.Code, rec.Body.String())

		var resp auth.LoginResponse
		r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
		r.NotEmpty(resp.RefreshToken, "the switched session must carry a refresh token")
		r.NotNil(resp.Organization)
		r.Equal("acmetech", resp.Organization.Slug)

		switched, err := f.authSvc.ValidateToken(f.ctx, resp.AccessToken)
		r.NoError(err)
		r.Equal("acmetech", switched.OrgSlug)
		r.Equal(string(models.MemberRoleAdmin), switched.Role)
	})

	t.Run("non-member org answers the anti-enumeration 401", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		rec := call(t, "demo")
		r.Equal(http.StatusUnauthorized, rec.Code, rec.Body.String())
		r.Equal(string(base.ErrorCodeInvalidCredentials), decodeErrorCode(t, rec))
	})
}
