package app

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

const acceptInvitePath = "/api/v1/auth/accept-invite"

// inviteIntoOrgB creates an invitation into org B for email, issued by a
// fresh admin of org B, and returns its token.
func (e *impersonationEnv) inviteIntoOrgB(email string) string {
	e.t.Helper()
	r := require.New(e.t)
	ctx := context.Background()

	inviter := models.NewUser("inviter-" + time.Now().Format("150405.000000000") + "@acme.com")
	r.NoError(e.server.dbService.CreateUser(ctx, inviter))
	r.NoError(e.server.dbService.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(e.orgB.UID, inviter.UID, models.MemberRoleAdmin)))

	resp, err := e.server.authService.CreateInvitation(ctx, e.orgB.Slug, inviter.UID, auth.InviteRequest{
		Email: email, Role: string(models.MemberRoleUser), ExpiresIn: "24h", App: "dash0",
	})
	r.NoError(err)

	return resp.Token
}

// TestAcceptInviteRouteAuthentication drives POST /api/v1/auth/accept-invite
// through the real route table (spec 2026-10-16-06): optional authentication,
// no anonymous fallback for a bad credential, no impersonation.
func TestAcceptInviteRouteAuthentication(t *testing.T) {
	t.Parallel()

	t.Run("impersonation token is refused", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		env := newImpersonationEnv(t, true)

		status, body := env.do(http.MethodPost, env.impersonatePath(), env.adminToken, `{"orgSlug":"impa"}`)
		r.Equal(http.StatusOK, status)
		impToken, ok := body["accessToken"].(string)
		r.True(ok)

		token := env.inviteIntoOrgB(env.member.Email)
		status, body = env.do(http.MethodPost, acceptInvitePath, impToken, `{"token":"`+token+`"}`)
		r.Equal(http.StatusForbidden, status)
		r.Equal(string(base.ErrorCodeImpersonationForbidden), body["code"])

		_, err := env.server.dbService.GetMemberByUserAndOrg(context.Background(), env.member.UID, env.orgB.UID)
		r.Error(err, "no membership may be created under impersonation")
	})

	t.Run("garbage bearer gets 401, no anonymous fallback", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		env := newImpersonationEnv(t, false)

		token := env.inviteIntoOrgB("newcomer@acme.com")
		status, body := env.do(http.MethodPost, acceptInvitePath, "not-a-jwt",
			`{"token":"`+token+`","name":"N","password":"newcomer-secret-1"}`)
		r.Equal(http.StatusUnauthorized, status)
		r.Equal(string(base.ErrorCodeInvalidToken), body["code"])
		r.NotContains(body, "accessToken")
	})

	t.Run("anonymous on an existing account gets INVITE_LOGIN_REQUIRED", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		env := newImpersonationEnv(t, false)

		token := env.inviteIntoOrgB(env.member.Email)
		status, body := env.do(http.MethodPost, acceptInvitePath, "", `{"token":"`+token+`"}`)
		r.Equal(http.StatusUnauthorized, status)
		r.Equal(string(base.ErrorCodeInviteLoginRequired), body["code"])
	})

	t.Run("the invited account, signed in, accepts", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		env := newImpersonationEnv(t, false)

		token := env.inviteIntoOrgB(env.member.Email)
		status, body := env.do(http.MethodPost, acceptInvitePath, env.memberToken, `{"token":"`+token+`"}`)
		r.Equal(http.StatusOK, status)
		r.NotEmpty(body["accessToken"])
	})

	t.Run("no credential and a new email: 200", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		env := newImpersonationEnv(t, false)

		token := env.inviteIntoOrgB("newcomer@acme.com")
		status, body := env.do(http.MethodPost, acceptInvitePath, "",
			`{"token":"`+token+`","name":"N","password":"newcomer-secret-1"}`)
		r.Equal(http.StatusOK, status)
		r.NotEmpty(body["accessToken"])
	})
}
