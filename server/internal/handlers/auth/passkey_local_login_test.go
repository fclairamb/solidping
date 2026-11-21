package auth_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// Local login off (spec 2026-11-21-02) does not touch passkeys: the password
// login is refused, the passkey one still mints a session.
func TestPasskeyLoginWorksWithLocalLoginDisabled(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	user := models.NewUser("alice@example.com")
	pwd, err := passwords.Hash(passkeyTestPassword)
	r.NoError(err)
	user.PasswordHash = &pwd
	r.NoError(dbSvc.CreateUser(ctx, user))

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	r.NoError(dbSvc.CreateOrganizationMember(ctx, models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	cfg := &config.Config{
		Auth: config.AuthConfig{
			JWTSecret:         "test-secret-1234567890",
			LocalLoginEnabled: false,
			WebAuthn:          config.WebAuthnConfig{Enabled: true},
		},
		Google: config.GoogleOAuthConfig{Enabled: true, ClientID: "id", ClientSecret: "secret"},
		Server: config.ServerConfig{BaseURL: passkeyTestOrigin},
	}

	authSvc := auth.NewService(dbSvc, cfg.Auth, cfg, nil, nil)
	pkSvc := auth.NewPasskeyService(authSvc, dbSvc)

	_, err = authSvc.Login(ctx, "acme", user.Email, passkeyTestPassword, auth.Context{})
	r.ErrorIs(err, auth.ErrLocalLoginDisabled)

	device := newSoftAuthenticator(t)
	begin, err := pkSvc.BeginRegistration(ctx, user.UID, passkeyTestPasswordPtr())
	r.NoError(err)
	created := device.register(t, begin.Options.Response.Challenge.String())
	_, err = pkSvc.FinishRegistration(ctx, begin.Session, created, "")
	r.NoError(err)

	login, err := pkSvc.BeginLogin(ctx, user.Email)
	r.NoError(err)
	resp, err := pkSvc.FinishLogin(ctx, login.Session,
		device.assert(t, login.Options.Response.Challenge.String(), []byte(user.UID)), "acme", auth.Context{})
	r.NoError(err)
	r.NotEmpty(resp.AccessToken)
}
