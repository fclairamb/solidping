package auth

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/testsupport"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

type faultWorld struct {
	svc   *Service
	dbSvc db.Service
	ctx   context.Context //nolint:containedctx // test fixture
	hook  *testsupport.FaultHook
	n     atomic.Int64
}

func newFaultWorld(t *testing.T) *faultWorld {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{BaseURL: "http://127.0.0.1:4000"},
		Auth: config.AuthConfig{
			JWTSecret:                "fault-jwt-secret",
			AccessTokenExpiry:        time.Hour,
			RefreshTokenExpiry:       7 * 24 * time.Hour,
			RegistrationEmailPattern: ".*@acme.com",
		},
	}

	return &faultWorld{
		svc:   NewService(dbSvc, cfg.Auth, cfg, nil, nil),
		dbSvc: dbSvc,
		ctx:   ctx,
		hook:  testsupport.InstallFaults(dbSvc.DB()),
	}
}

// member seeds a fresh org, owner and logged-in session (unarmed).
func (w *faultWorld) member(t *testing.T, role models.MemberRole) (string, *models.User, *LoginResponse) {
	t.Helper()

	id := w.n.Add(1)
	slug := fmt.Sprintf("fault-%d", id)
	email := fmt.Sprintf("user%d@acme.com", id)

	org := models.NewOrganization(slug, "Fault "+slug)
	require.NoError(t, w.dbSvc.CreateOrganization(w.ctx, org))

	hash, err := passwords.Hash("testpass1234")
	require.NoError(t, err)

	user := models.NewUser(email)
	user.PasswordHash = &hash
	require.NoError(t, w.dbSvc.CreateUser(w.ctx, user))
	require.NoError(t, w.dbSvc.CreateOrganizationMember(w.ctx, models.NewOrganizationMember(org.UID, user.UID, role)))

	login, err := w.svc.Login(w.ctx, slug, email, "testpass1234", Context{UserAgent: "ua", RemoteAddr: "127.0.0.1"})
	require.NoError(t, err)

	return slug, user, login
}

// TestServiceSurvivesAFailingQuery replays happy-path service operations once
// per database query, failing a different one each time: a failed store must
// surface as an error, never a panic.
func TestServiceSurvivesAFailingQuery(t *testing.T) {
	t.Parallel()

	names := make([]string, 0, 20)
	for name := range faultScenarios(t, newFaultWorld(t)) {
		names = append(names, name)
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newFaultWorld(t)
			scenario := faultScenarios(t, w)[name]
			w.hook.Sweep(t, scenario)
		})
	}
}

func faultScenarios(t *testing.T, w *faultWorld) map[string]func(arm func()) {
	t.Helper()

	owner := models.MemberRoleOwner
	ac := Context{UserAgent: "ua", RemoteAddr: "127.0.0.1"}

	return map[string]func(arm func()){
		"login": func(arm func()) {
			slug, user, _ := w.member(t, owner)
			arm()
			_, _ = w.svc.Login(w.ctx, slug, user.Email, "testpass1234", ac)
		},
		"login-bad-password": func(arm func()) {
			slug, user, _ := w.member(t, owner)
			arm()
			_, _ = w.svc.Login(w.ctx, slug, user.Email, "wrong-password", ac)
		},
		"refresh": func(arm func()) {
			_, _, login := w.member(t, owner)
			arm()
			_, _ = w.svc.Refresh(w.ctx, login.RefreshToken)
		},
		"logout": func(arm func()) {
			_, _, login := w.member(t, owner)
			arm()
			_ = w.svc.Logout(w.ctx, login.RefreshToken)
		},
		"logout-user": func(arm func()) {
			_, user, _ := w.member(t, owner)
			arm()
			_, _ = w.svc.LogoutUser(w.ctx, user.UID)
		},
		"logout-others": func(arm func()) {
			_, user, login := w.member(t, owner)
			tok, err := w.dbSvc.GetUserTokenByToken(w.ctx, login.RefreshToken)
			require.NoError(t, err)
			arm()
			_, _ = w.svc.LogoutOtherSessions(w.ctx, user.UID, tok.UID)
			_ = w.svc.LogoutSession(w.ctx, user.UID, tok.UID)
		},
		"validate": func(arm func()) {
			_, _, login := w.member(t, owner)
			arm()
			claims, err := w.svc.ValidateToken(w.ctx, login.AccessToken)
			if err == nil {
				_, _ = w.svc.GetUserInfo(w.ctx, claims)
			}
		},
		"profile": func(arm func()) {
			slug, user, login := w.member(t, owner)
			claims, err := w.svc.ValidateToken(w.ctx, login.AccessToken)
			require.NoError(t, err)
			name := "New Name"
			arm()
			_, _ = w.svc.UpdateProfile(w.ctx, claims, UpdateProfileRequest{Name: &name})
			_, _ = w.svc.GetUserInfo(w.ctx, claims)
			_, _ = w.svc.SwitchOrg(w.ctx, user.UID, slug, ac)
		},
		"pat": func(arm func()) {
			slug, user, login := w.member(t, owner)
			tok, err := w.dbSvc.GetUserTokenByToken(w.ctx, login.RefreshToken)
			require.NoError(t, err)
			arm()
			created, err := w.svc.CreatePAT(w.ctx, slug, user.UID, CreateTokenRequest{Name: "ci"})
			_, _ = w.svc.GetUserTokens(w.ctx, slug, user.UID, "", tok.UID)
			_, _ = w.svc.GetAllUserTokens(w.ctx, user.UID, "", tok.UID)
			if err == nil {
				_, _ = w.svc.ValidatePATToken(w.ctx, created.Token)
				_ = w.svc.RevokeToken(w.ctx, user.UID, created.UID)
			}
		},
		"mcp-token": func(arm func()) {
			slug, user, login := w.member(t, owner)
			tok, err := w.dbSvc.GetUserTokenByToken(w.ctx, login.RefreshToken)
			require.NoError(t, err)
			arm()
			_, _ = w.svc.GenerateMCPAccessToken(w.ctx, user.UID, slug, []string{"checks:read"}, "aud",
				time.Hour, tok.UID, "client")
		},
		"change-password": func(arm func()) {
			_, user, login := w.member(t, owner)
			tok, err := w.dbSvc.GetUserTokenByToken(w.ctx, login.RefreshToken)
			require.NoError(t, err)
			arm()
			_, _ = w.svc.ChangePassword(w.ctx, user.UID, tok.UID,
				ChangePasswordRequest{CurrentPassword: "testpass1234", NewPassword: "brandnewpassword"})
		},
		"password-reset": func(arm func()) {
			_, user, _ := w.member(t, owner)
			arm()
			_, _ = w.svc.RequestPasswordReset(w.ctx, RequestPasswordResetRequest{Email: user.Email}, "10.0.0.1")
			_, _ = w.svc.ResetPassword(w.ctx, ResetPasswordRequest{Token: "nope", Password: "brandnewpassword"})
		},
		"register": func(arm func()) {
			id := w.n.Add(1)
			arm()
			resp, err := w.svc.Register(w.ctx, RegisterRequest{
				Name: "Reg", Email: fmt.Sprintf("reg%d@acme.com", id), Password: "testpass1234",
			})
			if err == nil && resp != nil {
				_, _ = w.svc.ConfirmRegistration(w.ctx, "bogus")
			}
		},
		"create-org": func(arm func()) {
			_, user, _ := w.member(t, owner)
			arm()
			_, _ = w.svc.CreateOrg(w.ctx, user.UID, CreateOrgRequest{Name: "Second Org"}, ac)
		},
		"invitations": func(arm func()) {
			slug, user, _ := w.member(t, owner)
			arm()
			inv, err := w.svc.CreateInvitation(w.ctx, slug, user.UID, InviteRequest{
				Email: fmt.Sprintf("inv%d@acme.com", w.n.Add(1)), Role: "user",
			})
			_, _ = w.svc.ListInvitations(w.ctx, slug)
			if err == nil {
				_, _ = w.svc.GetInviteInfo(w.ctx, inv.Token)
				_, _ = w.svc.AcceptInvite(w.ctx, AcceptInviteRequest{
					Token: inv.Token, Name: "Invitee", Password: "testpass1234",
				}, "")
				_ = w.svc.RevokeInvitation(w.ctx, slug, inv.UID)
			}
		},
		"org-settings": func(arm func()) {
			slug, _, _ := w.member(t, owner)
			arm()
			_, _ = w.svc.GetOrgSettings(w.ctx, slug)
			on := true
			_, _ = w.svc.UpdateOrgSettings(w.ctx, slug, UpdateOrgSettingsRequest{TracerouteOnFailure: &on})
		},
		"two-factor": func(arm func()) {
			slug, user, _ := w.member(t, owner)
			arm()
			pw := "testpass1234"
			setup, err := w.svc.Setup2FA(w.ctx, user.UID, Setup2FARequest{CurrentPassword: &pw})
			if err != nil {
				return
			}
			code, codeErr := totp.GenerateCode(setup.Secret, time.Now())
			require.NoError(t, codeErr)
			confirm, err := w.svc.Confirm2FA(w.ctx, user.UID, Confirm2FARequest{Code: code, SetupToken: setup.SetupToken})
			if err != nil {
				return
			}
			_ = slug
			_, _ = w.svc.Recovery2FA(w.ctx, "bogus", confirm.RecoveryCodes[0], ac)
			_ = w.svc.Disable2FA(w.ctx, user.UID, code)
		},
	}
}
