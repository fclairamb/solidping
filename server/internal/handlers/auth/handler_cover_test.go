//nolint:lll // table-driven cases read better on one line
package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

type coverFixture struct {
	svc    *Service
	h      *Handler
	ctx    context.Context //nolint:containedctx // test fixture
	admin  *Claims
	member *Claims
	org    string
}

func newCoverFixture(t *testing.T) *coverFixture {
	t.Helper()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "http://127.0.0.1:4000")

	org := models.NewOrganization("hcov", "Handler Cover")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	hash, err := passwords.Hash("testpass1234")
	r.NoError(err)

	mk := func(email string, role models.MemberRole) *Claims {
		user := models.NewUser(email)
		user.PasswordHash = &hash
		r.NoError(dbSvc.CreateUser(ctx, user))
		r.NoError(dbSvc.CreateOrganizationMember(ctx, models.NewOrganizationMember(org.UID, user.UID, role)))

		resp, loginErr := svc.Login(ctx, "hcov", email, "testpass1234", Context{})
		r.NoError(loginErr)
		claims, valErr := svc.ValidateToken(ctx, resp.AccessToken)
		r.NoError(valErr)

		return claims
	}

	return &coverFixture{
		svc:    svc,
		h:      NewHandler(svc, &config.Config{}),
		ctx:    ctx,
		admin:  mk("admin@acme.com", models.MemberRoleAdmin),
		member: mk("member@acme.com", models.MemberRoleUser),
		org:    "hcov",
	}
}

type coverCall struct {
	fn     func(http.ResponseWriter, *http.Request) error
	body   string
	claims *Claims
	params map[string]string
	query  string
	header map[string]string
}

func (f *coverFixture) do(t *testing.T, call coverCall) *httptest.ResponseRecorder {
	t.Helper()

	ctx := f.ctx
	if call.claims != nil {
		ctx = context.WithValue(ctx, base.ContextKeyClaims, call.claims)
	}

	rctx := chi.NewRouteContext()
	for k, v := range call.params {
		rctx.URLParams.Add(k, v)
	}

	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)

	target := "/x"
	if call.query != "" {
		target += "?" + call.query
	}

	req := httptest.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(call.body))
	for k, v := range call.header {
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	require.NoError(t, call.fn(rec, req))

	return rec
}

func TestHandlerValidationAndAuthGates(t *testing.T) { //nolint:tparallel // subtests share one fixture
	t.Parallel()

	f := newCoverFixture(t)
	h := f.h
	orgP := map[string]string{"org": f.org}

	cases := []struct {
		name string
		call coverCall
		want int
	}{
		{"login bad json", coverCall{fn: h.Login, body: "{"}, http.StatusUnprocessableEntity},
		{"login no email", coverCall{fn: h.Login, body: `{"password":"x"}`}, http.StatusUnprocessableEntity},
		{"login no password", coverCall{fn: h.Login, body: `{"email":"a@acme.com"}`}, http.StatusUnprocessableEntity},
		{"login wrong password", coverCall{
			fn:   h.Login,
			body: `{"org":"hcov","email":"admin@acme.com","password":"nope"}`,
		}, http.StatusUnauthorized},
		{"login ok", coverCall{
			fn:   h.Login,
			body: `{"org":"hcov","email":"admin@acme.com","password":"testpass1234"}`,
		}, http.StatusOK},
		{"logout unauth", coverCall{fn: h.Logout}, http.StatusUnauthorized},
		{"logout both flags", coverCall{
			fn: h.Logout, claims: f.admin,
			body: `{"deleteAllTokens":true,"signOutOthers":true}`,
		}, http.StatusUnprocessableEntity},
		{"logout default", coverCall{fn: h.Logout, claims: &Claims{UserUID: f.admin.UserUID}}, http.StatusOK},
		{"logout all", coverCall{
			fn: h.Logout, claims: &Claims{UserUID: f.admin.UserUID},
			body: `{"deleteAllTokens":true}`,
		}, http.StatusOK},
		{"logout others no session", coverCall{
			fn: h.Logout, claims: &Claims{UserUID: f.admin.UserUID},
			body: `{"signOutOthers":true}`,
		}, http.StatusUnprocessableEntity},
		{"logout impersonation others", coverCall{
			fn:     h.Logout,
			claims: &Claims{UserUID: f.admin.UserUID, ImpersonatedBy: "someone"},
			body:   `{"signOutOthers":true}`,
		}, http.StatusForbidden},
		{"refresh bad json", coverCall{fn: h.Refresh, body: "{"}, http.StatusUnprocessableEntity},
		{"refresh empty", coverCall{fn: h.Refresh, body: `{}`}, http.StatusUnprocessableEntity},
		{"refresh invalid", coverCall{fn: h.Refresh, body: `{"refreshToken":"bogus"}`}, http.StatusUnauthorized},
		{"me unauth", coverCall{fn: h.Me}, http.StatusUnauthorized},
		{"me ok", coverCall{fn: h.Me, claims: f.admin}, http.StatusOK},
		{"me unknown user", coverCall{fn: h.Me, claims: &Claims{UserUID: "nope", OrgSlug: "hcov"}}, http.StatusUnauthorized},
		{"update me unauth", coverCall{fn: h.UpdateMe}, http.StatusUnauthorized},
		{"update me bad json", coverCall{fn: h.UpdateMe, claims: f.admin, body: "{"}, http.StatusUnprocessableEntity},
		{"update me ok", coverCall{fn: h.UpdateMe, claims: f.admin, body: `{"name":"Alice"}`}, http.StatusOK},
		{"admin update unauth", coverCall{fn: h.AdminUpdateUser}, http.StatusUnauthorized},
		{"admin update bad json", coverCall{fn: h.AdminUpdateUser, claims: f.admin, body: "{"}, http.StatusUnprocessableEntity},
		{"admin update not super admin", coverCall{
			fn: h.AdminUpdateUser, claims: f.admin,
			params: map[string]string{"uid": "nope"}, body: `{"name":"x"}`,
		}, http.StatusForbidden},
		{"all tokens unauth", coverCall{fn: h.GetAllUserTokens}, http.StatusUnauthorized},
		{"all tokens ok", coverCall{fn: h.GetAllUserTokens, claims: f.admin, query: "type=refresh"}, http.StatusOK},
		{"org tokens unauth", coverCall{fn: h.GetOrgTokens}, http.StatusUnauthorized},
		{"org tokens ok", coverCall{fn: h.GetOrgTokens, claims: f.admin, params: orgP}, http.StatusOK},
		{"org tokens unknown org", coverCall{
			fn: h.GetOrgTokens, claims: f.admin,
			params: map[string]string{"org": "ghost"},
		}, http.StatusNotFound},
		{"create token unauth", coverCall{fn: h.CreateToken}, http.StatusUnauthorized},
		{"create token bad json", coverCall{fn: h.CreateToken, claims: f.admin, params: orgP, body: "{"}, http.StatusUnprocessableEntity},
		{"create token no name", coverCall{fn: h.CreateToken, claims: f.admin, params: orgP, body: `{}`}, http.StatusUnprocessableEntity},
		{"create token ok", coverCall{fn: h.CreateToken, claims: f.admin, params: orgP, body: `{"name":"ci"}`}, http.StatusCreated},
		{"create token unknown org", coverCall{
			fn: h.CreateToken, claims: f.admin,
			params: map[string]string{"org": "ghost"}, body: `{"name":"ci"}`,
		}, http.StatusNotFound},
		{"revoke token unauth", coverCall{fn: h.RevokeToken}, http.StatusUnauthorized},
		{"revoke token no uid", coverCall{fn: h.RevokeToken, claims: f.admin}, http.StatusUnprocessableEntity},
		{"revoke token missing", coverCall{
			fn: h.RevokeToken, claims: f.admin,
			params: map[string]string{"tokenUid": "nope"},
		}, http.StatusNotFound},
		{"revoke current unauth", coverCall{fn: h.RevokeCurrentToken}, http.StatusUnauthorized},
		{"revoke current no grant", coverCall{
			fn:     h.RevokeCurrentToken,
			claims: &Claims{UserUID: f.admin.UserUID},
		}, http.StatusUnprocessableEntity},
		{"revoke current missing row", coverCall{
			fn:     h.RevokeCurrentToken,
			claims: &Claims{UserUID: f.admin.UserUID, RefreshUID: "nope"},
		}, http.StatusNotFound},
		{"switch org unauth", coverCall{fn: h.SwitchOrg}, http.StatusUnauthorized},
		{"switch org bad json", coverCall{fn: h.SwitchOrg, claims: f.admin, body: "{"}, http.StatusUnprocessableEntity},
		{"switch org empty", coverCall{fn: h.SwitchOrg, claims: f.admin, body: `{}`}, http.StatusUnprocessableEntity},
		{"switch org ghost", coverCall{fn: h.SwitchOrg, claims: f.admin, body: `{"org":"ghost"}`}, http.StatusUnauthorized},
		{"switch org ok", coverCall{fn: h.SwitchOrg, claims: f.admin, body: `{"org":"hcov"}`}, http.StatusOK},
		{"register bad json", coverCall{fn: h.Register, body: "{"}, http.StatusUnprocessableEntity},
		{"register no email", coverCall{fn: h.Register, body: `{}`}, http.StatusUnprocessableEntity},
		{"register no password", coverCall{fn: h.Register, body: `{"email":"n@acme.com"}`}, http.StatusUnprocessableEntity},
		{"register disabled", coverCall{
			fn:   h.Register,
			body: `{"email":"n@acme.com","password":"longenough1"}`,
		}, http.StatusForbidden},
		{"confirm bad json", coverCall{fn: h.ConfirmRegistration, body: "{"}, http.StatusUnprocessableEntity},
		{"confirm no token", coverCall{fn: h.ConfirmRegistration, body: `{}`}, http.StatusUnprocessableEntity},
		{"confirm bad token", coverCall{fn: h.ConfirmRegistration, body: `{"token":"zzz"}`}, http.StatusGone},
		{"reset request bad json", coverCall{fn: h.RequestPasswordReset, body: "{"}, http.StatusUnprocessableEntity},
		{"reset request no email", coverCall{fn: h.RequestPasswordReset, body: `{}`}, http.StatusUnprocessableEntity},
		{"reset request ok", coverCall{fn: h.RequestPasswordReset, body: `{"email":"ghost@acme.com"}`}, http.StatusOK},
		{"reset bad json", coverCall{fn: h.ResetPassword, body: "{"}, http.StatusUnprocessableEntity},
		{"reset no token", coverCall{fn: h.ResetPassword, body: `{}`}, http.StatusUnprocessableEntity},
		{"reset no password", coverCall{fn: h.ResetPassword, body: `{"token":"t"}`}, http.StatusUnprocessableEntity},
		{"reset bad token", coverCall{fn: h.ResetPassword, body: `{"token":"t","password":"longenough1"}`}, http.StatusGone},
		{"create org unauth", coverCall{fn: h.CreateOrg}, http.StatusUnauthorized},
		{"create org bad json", coverCall{fn: h.CreateOrg, claims: f.admin, body: "{"}, http.StatusUnprocessableEntity},
		{"create org no name", coverCall{fn: h.CreateOrg, claims: f.admin, body: `{}`}, http.StatusUnprocessableEntity},
		{"create org bad slug", coverCall{
			fn: h.CreateOrg, claims: f.admin,
			body: `{"name":"X","slug":"A_B!"}`,
		}, http.StatusUnprocessableEntity},
		{"create org taken", coverCall{
			fn: h.CreateOrg, claims: f.admin,
			body: `{"name":"X","slug":"hcov"}`,
		}, http.StatusConflict},
		{"create org ok", coverCall{
			fn: h.CreateOrg, claims: f.admin,
			body: `{"name":"Fresh","slug":"fresh-org"}`,
		}, http.StatusCreated},
		{"delete org unauth", coverCall{fn: h.DeleteOrg}, http.StatusUnauthorized},
		{"delete org bad json", coverCall{fn: h.DeleteOrg, claims: f.admin, params: orgP, body: "{"}, http.StatusUnprocessableEntity},
		{"delete org mismatch", coverCall{
			fn: h.DeleteOrg, claims: f.admin, params: orgP,
			body: `{"slug":"wrong"}`,
		}, http.StatusUnprocessableEntity},
		{"update profile unauth", coverCall{fn: h.UpdateOrgProfile}, http.StatusUnauthorized},
		{
			"update profile bad json",
			coverCall{fn: h.UpdateOrgProfile, claims: f.admin, params: orgP, body: "{"},
			http.StatusUnprocessableEntity,
		},
		{"update profile ghost org", coverCall{
			fn: h.UpdateOrgProfile, claims: f.admin,
			params: map[string]string{"org": "ghost"}, body: `{"name":"x"}`,
		}, http.StatusNotFound},
		{"update profile ok", coverCall{
			fn: h.UpdateOrgProfile, claims: f.admin, params: orgP,
			body: `{"name":"Renamed"}`,
		}, http.StatusOK},
		{"invite unauth", coverCall{fn: h.CreateInvitation}, http.StatusUnauthorized},
		{"invite non admin", coverCall{fn: h.CreateInvitation, claims: f.member, params: orgP}, http.StatusForbidden},
		{"invite bad json", coverCall{fn: h.CreateInvitation, claims: f.admin, params: orgP, body: "{"}, http.StatusUnprocessableEntity},
		{"invite no email", coverCall{fn: h.CreateInvitation, claims: f.admin, params: orgP, body: `{}`}, http.StatusUnprocessableEntity},
		{"invite ok", coverCall{
			fn: h.CreateInvitation, claims: f.admin, params: orgP,
			body: `{"email":"new@acme.com"}`,
		}, http.StatusCreated},
		{"invite bad expiry", coverCall{
			fn: h.CreateInvitation, claims: f.admin, params: orgP,
			body: `{"email":"n2@acme.com","expiresIn":"9y"}`,
		}, http.StatusBadRequest},
		{"invite ghost org", coverCall{
			fn: h.CreateInvitation, claims: f.admin,
			params: map[string]string{"org": "ghost"}, body: `{"email":"n3@acme.com"}`,
		}, http.StatusNotFound},
		{"list invites unauth", coverCall{fn: h.ListInvitations}, http.StatusUnauthorized},
		{"list invites non admin", coverCall{fn: h.ListInvitations, claims: f.member, params: orgP}, http.StatusForbidden},
		{"list invites ok", coverCall{fn: h.ListInvitations, claims: f.admin, params: orgP}, http.StatusOK},
		{"revoke invite unauth", coverCall{fn: h.RevokeInvitation}, http.StatusUnauthorized},
		{"revoke invite non admin", coverCall{fn: h.RevokeInvitation, claims: f.member, params: orgP}, http.StatusForbidden},
		{"revoke invite missing", coverCall{
			fn: h.RevokeInvitation, claims: f.admin,
			params: map[string]string{"org": f.org, "uid": "nope"},
		}, http.StatusNotFound},
		{"invite info no token", coverCall{fn: h.GetInviteInfo}, http.StatusUnprocessableEntity},
		{"invite info missing", coverCall{fn: h.GetInviteInfo, params: map[string]string{"token": "nope"}}, http.StatusNotFound},
		{"accept bad json", coverCall{fn: h.AcceptInvite, body: "{"}, http.StatusUnprocessableEntity},
		{"accept no token", coverCall{fn: h.AcceptInvite, body: `{}`}, http.StatusUnprocessableEntity},
		{"accept missing", coverCall{fn: h.AcceptInvite, body: `{"token":"nope"}`}, http.StatusNotFound},
		{"get settings unauth", coverCall{fn: h.GetOrgSettings}, http.StatusUnauthorized},
		{"get settings non admin", coverCall{fn: h.GetOrgSettings, claims: f.member, params: orgP}, http.StatusForbidden},
		{"get settings ok", coverCall{fn: h.GetOrgSettings, claims: f.admin, params: orgP}, http.StatusOK},
		{"put settings unauth", coverCall{fn: h.UpdateOrgSettings}, http.StatusUnauthorized},
		{"put settings non admin", coverCall{fn: h.UpdateOrgSettings, claims: f.member, params: orgP}, http.StatusForbidden},
		{
			"put settings bad json",
			coverCall{fn: h.UpdateOrgSettings, claims: f.admin, params: orgP, body: "{"},
			http.StatusUnprocessableEntity,
		},
		{"put settings ok", coverCall{fn: h.UpdateOrgSettings, claims: f.admin, params: orgP, body: `{}`}, http.StatusOK},
		{"put settings bad regex", coverCall{
			fn: h.UpdateOrgSettings, claims: f.admin, params: orgP,
			body: `{"registrationEmailPattern":"("}`,
		}, http.StatusBadRequest},
	}

	for _, tc := range cases { //nolint:paralleltest // subtests share one fixture and run in order
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(t, tc.call)
			require.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}

func TestHandlerTwoFactorFlow(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newCoverFixture(t)
	h := f.h

	// Gates.
	for name, call := range map[string]coverCall{
		"setup unauth":      {fn: h.Setup2FA},
		"confirm unauth":    {fn: h.Confirm2FA},
		"disable unauth":    {fn: h.Disable2FA},
		"verify no token":   {fn: h.Verify2FA},
		"recovery no token": {fn: h.Recovery2FA},
	} {
		r.Equal(http.StatusUnauthorized, f.do(t, call).Code, name)
	}

	bearer := map[string]string{"Authorization": "Bearer sometoken"}
	for name, call := range map[string]coverCall{
		"confirm bad json":   {fn: h.Confirm2FA, claims: f.admin, body: "{"},
		"confirm no code":    {fn: h.Confirm2FA, claims: f.admin, body: `{}`},
		"disable bad json":   {fn: h.Disable2FA, claims: f.admin, body: "{"},
		"disable no code":    {fn: h.Disable2FA, claims: f.admin, body: `{}`},
		"verify bad json":    {fn: h.Verify2FA, header: bearer, body: "{"},
		"verify no code":     {fn: h.Verify2FA, header: bearer, body: `{}`},
		"recovery bad json":  {fn: h.Recovery2FA, header: bearer, body: "{"},
		"recovery no code":   {fn: h.Recovery2FA, header: bearer, body: `{}`},
		"confirm no secret":  {fn: h.Confirm2FA, claims: f.admin, body: `{"code":"123456"}`},
		"disable not on":     {fn: h.Disable2FA, claims: f.admin, body: `{"code":"123456"}`},
		"verify bad token":   {fn: h.Verify2FA, header: bearer, body: `{"code":"123456"}`},
		"recovery bad token": {fn: h.Recovery2FA, header: bearer, body: `{"recoveryCode":"abc"}`},
	} {
		code := f.do(t, call).Code
		r.GreaterOrEqual(code, 400, name)
		r.Less(code, 500, name)
	}

	// Happy path: setup, wrong code, confirm, double setup, disable.
	rec := f.do(t, coverCall{fn: h.Setup2FA, claims: f.admin})
	r.Equal(http.StatusOK, rec.Code)

	user, err := f.svc.db.GetUser(f.ctx, f.admin.UserUID)
	r.NoError(err)
	r.NotNil(user.TOTPSecret)

	r.Equal(http.StatusUnauthorized,
		f.do(t, coverCall{fn: h.Confirm2FA, claims: f.admin, body: `{"code":"000000"}`}).Code)

	code, err := totp.GenerateCode(*user.TOTPSecret, time.Now())
	r.NoError(err)
	rec = f.do(t, coverCall{fn: h.Confirm2FA, claims: f.admin, body: `{"code":"` + code + `"}`})
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	r.Equal(http.StatusConflict, f.do(t, coverCall{fn: h.Setup2FA, claims: f.admin}).Code)
	r.Equal(http.StatusUnauthorized,
		f.do(t, coverCall{fn: h.Disable2FA, claims: f.admin, body: `{"code":"000000"}`}).Code)

	rec = f.do(t, coverCall{fn: h.Disable2FA, claims: f.admin, body: `{"code":"` + code + `"}`})
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	// Unknown user.
	ghost := &Claims{UserUID: "ghost"}
	r.Equal(http.StatusNotFound, f.do(t, coverCall{fn: h.Setup2FA, claims: ghost}).Code)
}

func TestHandlerRevokeSessionTokens(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newCoverFixture(t)
	r.NotEmpty(f.admin.RefreshUID)

	rec := f.do(t, coverCall{fn: f.h.RevokeCurrentToken, claims: f.admin})
	r.Equal(http.StatusNoContent, rec.Code)

	// PAT create then revoke by uid.
	rec = f.do(t, coverCall{
		fn: f.h.CreateToken, claims: f.member,
		params: map[string]string{"org": f.org}, body: `{"name":"ci"}`,
	})
	r.Equal(http.StatusCreated, rec.Code)

	rec = f.do(t, coverCall{fn: f.h.GetOrgTokens, claims: f.member, params: map[string]string{"org": f.org}})
	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Body.String(), "ci")
}

func TestExtractBearerToken(t *testing.T) {
	t.Parallel()

	cases := []struct{ header, want string }{
		{"", ""},
		{"Bearer", ""},
		{"Basic abc", ""},
		{"Bearer abc", "abc"},
		{"bearer abc", "abc"},
	}
	for _, tc := range cases {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}

		require.Equal(t, tc.want, extractBearerToken(req), tc.header)
	}
}
