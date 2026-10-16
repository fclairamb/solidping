package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// TestFederatedTOTPTempTokenIsNoBearer pins the H1 dependency of spec
// 2026-10-16-14: the 2FA temp token a federated callback now hands to the
// browser must not open GET /api/v1/auth/me through the real RequireAuth
// middleware, or the challenge would protect nothing. The positive control is
// the session Verify2FA mints from that same temp token, which does.
func TestFederatedTOTPTempTokenIsNoBearer(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	cfg := &config.Config{
		Auth: config.AuthConfig{
			JWTSecret:          "federated-totp-bearer-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 24 * time.Hour,
		},
		Server: config.ServerConfig{BaseURL: "http://localhost:4000"},
	}

	svc := auth.NewService(dbSvc, cfg.Auth, cfg, nil, nil)

	org := models.NewOrganization("acmetech", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("alice@acme.com")
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	key, err := totp.Generate(totp.GenerateOpts{Issuer: "SolidPing Test", AccountName: user.Email})
	r.NoError(err)

	secret := key.Secret()
	enabled := true
	r.NoError(dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{TOTPSecret: &secret, TOTPEnabled: &enabled}))

	user.TOTPEnabled = true
	user.TOTPSecret = &secret

	login, err := svc.CompleteOrgLogin(ctx, org, user, auth.WithLoginMethod("oidc"))
	r.NoError(err)
	r.True(login.Requires2FA)
	r.Empty(login.AccessToken)

	authMW := middleware.NewAuthMiddleware(svc, dbSvc, cfg)
	handler := auth.NewHandler(svc, cfg)
	me := authMW.RequireAuth(handler.Me)

	callMe := func(bearer string) int {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/auth/me", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)

		rec := httptest.NewRecorder()
		r.NoError(me(rec, req))

		return rec.Code
	}

	r.Equal(http.StatusUnauthorized, callMe(login.TempToken),
		"a 2FA temp token from a federated callback must not work as a bearer token")

	code, err := totp.GenerateCode(secret, time.Now())
	r.NoError(err)

	session, err := svc.Verify2FA(ctx, login.TempToken, code, auth.Context{})
	r.NoError(err)
	r.Equal(http.StatusOK, callMe(session.AccessToken), "positive control: the verified session works")
}
