package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

const patBindingPassword = "alicepass1234"

// patBindingEnv is the real server with one user, admin of org A and org B,
// a PAT created in org A and a session from a password login (spec
// 2026-10-28-01).
type patBindingEnv struct {
	*impersonationEnv

	user    *models.User
	pat     string
	session string
}

func newPATBindingEnv(t *testing.T) *patBindingEnv {
	t.Helper()
	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Server.BaseURL = "http://localhost"
	cfg.Auth.JWTSecret = "pat-binding-secret"
	cfg.Auth.AccessTokenExpiry = time.Hour
	cfg.Auth.RefreshTokenExpiry = 24 * time.Hour
	cfg.Auth.WebAuthn.Enabled = true

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	env := &patBindingEnv{impersonationEnv: &impersonationEnv{t: t, server: server, ts: ts}}

	env.orgA = models.NewOrganization("pata", "Acme A")
	r.NoError(server.dbService.CreateOrganization(ctx, env.orgA))
	env.orgB = models.NewOrganization("patb", "Acme B")
	r.NoError(server.dbService.CreateOrganization(ctx, env.orgB))
	r.NoError(server.dbService.CreateCheck(ctx, models.NewCheck(env.orgA.UID, "a-check", "http")))
	r.NoError(server.dbService.CreateCheck(ctx, models.NewCheck(env.orgB.UID, "b-check", "http")))

	hash, err := passwords.Hash(patBindingPassword)
	r.NoError(err)

	env.user = models.NewUser("alice@acme.com")
	env.user.PasswordHash = &hash
	r.NoError(server.dbService.CreateUser(ctx, env.user))

	now := time.Now()
	for _, org := range []*models.Organization{env.orgA, env.orgB} {
		member := models.NewOrganizationMember(org.UID, env.user.UID, models.MemberRoleAdmin)
		member.JoinedAt = &now
		r.NoError(server.dbService.CreateOrganizationMember(ctx, member))
	}

	created, err := server.authService.CreatePAT(ctx, env.orgA.Slug, env.user.UID, auth.CreateTokenRequest{Name: "ci"})
	r.NoError(err)
	env.pat = created.Token

	status, body := env.do(http.MethodPost, "/api/v1/auth/login", "",
		`{"org":"pata","email":"alice@acme.com","password":"`+patBindingPassword+`"}`)
	r.Equal(http.StatusOK, status, body)
	env.session, _ = body["accessToken"].(string)
	r.NotEmpty(env.session)

	return env
}

func (e *patBindingEnv) tokenCount(tokenType models.TokenType) int {
	e.t.Helper()

	tokens, err := e.server.dbService.ListUserTokensByType(context.Background(), e.user.UID, tokenType)
	require.NoError(e.t, err)

	return len(tokens)
}

func (e *patBindingEnv) twoFACounterExists() bool {
	e.t.Helper()

	entry, err := e.server.dbService.GetStateEntry(context.Background(), nil, "twofa_attempt_count:"+e.user.UID)

	return err == nil && entry != nil
}

// TestPATCannotLeaveItsOrg is the regression for finding X1: every route that
// would let a PAT step out of its org binding answers 403 PAT_FORBIDDEN, and
// the same calls made from a password session still go through.
//
//nolint:paralleltest,tparallel // sub-tests share one server and its token rows
func TestPATCannotLeaveItsOrg(t *testing.T) {
	t.Parallel()
	env := newPATBindingEnv(t)

	forbidden := []struct {
		name, method, path, body string
	}{
		{"switch org", http.MethodPost, "/api/v1/auth/switch-org", `{"org":"patb"}`},
		{"org creation", http.MethodPost, "/api/v1/orgs", `{"slug":"patc","name":"Acme C"}`},
		{"accept invite", http.MethodPost, "/api/v1/auth/accept-invite", `{"token":"x"}`},
		{"change password", http.MethodPost, "/api/v1/auth/change-password", `{"newPassword":"hijack-1234567"}`},
		{"2FA setup", http.MethodPost, "/api/v1/auth/2fa/setup", `{}`},
		{"2FA confirm", http.MethodPost, "/api/v1/auth/2fa/confirm", `{"code":"123456"}`},
		{"2FA removal", http.MethodDelete, "/api/v1/auth/2fa", `{"code":"123456"}`},
		{"passkey add (begin)", http.MethodPost, "/api/v1/auth/passkeys/register/begin", `{}`},
		{"passkey add (finish)", http.MethodPost, "/api/v1/auth/passkeys/register/finish", `{}`},
		{"passkey removal", http.MethodDelete, "/api/v1/auth/passkeys/abc", ``},
		{
			"device consent", http.MethodPost, "/api/v1/auth/device/consent",
			`{"userCode":"ABCD-EFGH","org":"patb","approve":true}`,
		},
		{"discord link", http.MethodPost, "/api/v1/orgs/pata/users/me/discord/link-start", `{}`},
		{"discord connect", http.MethodPost, "/api/v1/orgs/pata/users/me/discord/connect", `{}`},
		{"telegram link", http.MethodPost, "/api/v1/orgs/pata/users/me/telegram/link", `{}`},
	}

	refreshBefore := env.tokenCount(models.TokenTypeRefresh)
	patBefore := env.tokenCount(models.TokenTypePAT)

	for _, call := range forbidden {
		t.Run(call.name, func(t *testing.T) {
			r := require.New(t)

			status, body := env.do(call.method, call.path, env.pat, call.body)
			r.Equal(http.StatusForbidden, status, body)
			r.Equal(string(base.ErrorCodePATForbidden), body["code"])
		})
	}

	r := require.New(t)
	r.Equal(refreshBefore, env.tokenCount(models.TokenTypeRefresh), "no session was minted")
	r.Equal(patBefore, env.tokenCount(models.TokenTypePAT), "no PAT was minted")
	r.False(env.twoFACounterExists(), "DELETE /auth/2fa spent no 2FA attempt")

	// The PAT still reads its own org, and only its own org.
	status, body := env.do(http.MethodGet, "/api/v1/orgs/pata/checks", env.pat, "")
	r.Equal(http.StatusOK, status, body)
	status, _ = env.do(http.MethodGet, "/api/v1/orgs/patb/checks", env.pat, "")
	r.Equal(http.StatusForbidden, status)

	// The PAT still creates a PAT in its own org.
	status, body = env.do(http.MethodPost, "/api/v1/orgs/pata/tokens", env.pat, `{"name":"second"}`)
	r.Equal(http.StatusCreated, status, body)

	// The PAT still edits the profile, but not the sign-in email.
	status, body = env.do(http.MethodPatch, "/api/v1/auth/me", env.pat, `{"name":"Alice"}`)
	r.Equal(http.StatusOK, status, body)
	status, body = env.do(http.MethodPatch, "/api/v1/auth/me", env.pat,
		`{"email":"mallory@acme.com","currentPassword":"`+patBindingPassword+`"}`)
	r.Equal(http.StatusForbidden, status, body)
	r.Equal(string(base.ErrorCodePATForbidden), body["code"])

	// Positive controls: the password session does what the PAT may not.
	status, body = env.do(http.MethodPost, "/api/v1/auth/passkeys/register/begin", env.session, `{}`)
	r.Equal(http.StatusOK, status, body)

	status, body = env.do(http.MethodPost, "/api/v1/auth/switch-org", env.session, `{"org":"patb"}`)
	r.Equal(http.StatusOK, status, body)
	r.Equal(refreshBefore+1, env.tokenCount(models.TokenTypeRefresh))
	switched, _ := body["accessToken"].(string)
	status, _ = env.do(http.MethodGet, "/api/v1/orgs/patb/checks", switched, "")
	r.Equal(http.StatusOK, status)

	// DELETE /auth/2fa from a session still spends the shared login budget
	// (spec 2026-10-27-02): 2FA is off, so the call is refused, but counted.
	status, _ = env.do(http.MethodDelete, "/api/v1/auth/2fa", env.session, `{"code":"123456"}`)
	r.NotEqual(http.StatusForbidden, status)
	r.True(env.twoFACounterExists())
}

// TestPATDenylistNamesRealRoutes keeps the denylist honest: every entry must
// be a registered route, or a renamed endpoint would silently fall out of the
// guard.
func TestPATDenylistNamesRealRoutes(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := newImpersonationEnv(t, true)

	registered := map[string]bool{}
	r.NoError(env.server.router.Walk(func(method, pattern string) error {
		registered[method+" "+pattern] = true

		return nil
	}))

	denylist := auth.PATForbiddenRoutes()
	r.NotEmpty(denylist)

	for _, entry := range denylist {
		r.Truef(registered[entry[0]+" "+entry[1]],
			"PAT denylist entry %s %s is not a registered route", entry[0], entry[1])
	}
}
