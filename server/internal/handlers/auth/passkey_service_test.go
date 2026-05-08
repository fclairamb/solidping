package auth_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

// passkeyTestUser is a minimal helper that wires up an authenticated
// user against an in-memory sqlite db.
func passkeyTestUser(t *testing.T) (
	*auth.PasskeyService, *sqlite.Service, *models.User,
) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	user := models.NewUser("alice@example.com")
	user.Name = "Alice"
	pwd := "$plaintext$pw"
	user.PasswordHash = &pwd
	r.NoError(dbSvc.CreateUser(ctx, user))

	cfg := &config.Config{
		Auth: config.AuthConfig{
			JWTSecret: "test-secret-1234567890",
			WebAuthn: config.WebAuthnConfig{
				Enabled: true,
			},
		},
		Server: config.ServerConfig{
			BaseURL: "https://example.com",
		},
	}

	authSvc := auth.NewService(dbSvc, cfg.Auth, cfg, nil)
	pkSvc := auth.NewPasskeyService(authSvc, dbSvc)

	return pkSvc, dbSvc, user
}

// TestPasskeyService_DisabledWhenBaseURLPlainHTTP confirms that a
// non-https base URL drops the service into disabled state. Browsers
// refuse WebAuthn over plain http (except localhost), so registering a
// credential the user can't use would be a footgun.
func TestPasskeyService_DisabledWhenBaseURLPlainHTTP(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	cfg := &config.Config{
		Auth: config.AuthConfig{
			JWTSecret: "test-secret-1234567890",
			WebAuthn:  config.WebAuthnConfig{Enabled: true},
		},
		Server: config.ServerConfig{BaseURL: "http://public.example.com"},
	}

	authSvc := auth.NewService(dbSvc, cfg.Auth, cfg, nil)
	pkSvc := auth.NewPasskeyService(authSvc, dbSvc)

	r.False(pkSvc.Enabled())
}

// TestPasskeyService_EnabledOnLocalhostHTTP ensures the localhost
// allowance — the standard self-host dev workflow — keeps passkeys
// enabled even without TLS.
func TestPasskeyService_EnabledOnLocalhostHTTP(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	cfg := &config.Config{
		Auth: config.AuthConfig{
			JWTSecret: "test-secret-1234567890",
			WebAuthn:  config.WebAuthnConfig{Enabled: true},
		},
		Server: config.ServerConfig{BaseURL: "http://localhost:4000"},
	}

	authSvc := auth.NewService(dbSvc, cfg.Auth, cfg, nil)
	pkSvc := auth.NewPasskeyService(authSvc, dbSvc)

	r.True(pkSvc.Enabled())
}

// TestPasskeyService_UsesOverlaidBaseURL pins the contract that the
// startup reorder relies on: NewPasskeyService reads BaseURL through
// authSvc.fullCfg at construction time, so a mutation between
// auth.NewService and auth.NewPasskeyService (which is what
// InitializeSystemConfig does between server.NewServer and
// server.SetupRoutes) determines the resulting RP ID. If this ever
// regresses to a snapshot-at-NewService pattern, the WebAuthn relying
// party would freeze on the wrong host again.
func TestPasskeyService_UsesOverlaidBaseURL(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	user := models.NewUser("alice@example.com")
	r.NoError(dbSvc.CreateUser(ctx, user))

	cfg := &config.Config{
		Auth: config.AuthConfig{
			JWTSecret: "test-secret-1234567890",
			WebAuthn:  config.WebAuthnConfig{Enabled: true},
		},
		Server: config.ServerConfig{BaseURL: "https://wrong.example.com"},
	}

	authSvc := auth.NewService(dbSvc, cfg.Auth, cfg, nil)

	// Simulate InitializeSystemConfig overlaying server.base_url from the
	// parameters table after the auth service exists but before the
	// PasskeyService is constructed.
	cfg.Server.BaseURL = "http://localhost:4000"

	pkSvc := auth.NewPasskeyService(authSvc, dbSvc)
	r.True(pkSvc.Enabled())

	resp, err := pkSvc.BeginRegistration(ctx, user.UID)
	r.NoError(err)
	r.NotNil(resp.Options)
	r.Equal("localhost", resp.Options.Response.RelyingParty.ID)
}

// TestPasskeyService_BeginRegistrationWhenDisabled returns
// ErrWebAuthnNotConfigured rather than a half-baked options blob.
func TestPasskeyService_BeginRegistrationWhenDisabled(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	cfg := &config.Config{
		Auth: config.AuthConfig{
			JWTSecret: "test-secret-1234567890",
			WebAuthn:  config.WebAuthnConfig{Enabled: false},
		},
		Server: config.ServerConfig{BaseURL: "https://example.com"},
	}

	authSvc := auth.NewService(dbSvc, cfg.Auth, cfg, nil)
	pkSvc := auth.NewPasskeyService(authSvc, dbSvc)

	user := models.NewUser("a@b.com")
	r.NoError(dbSvc.CreateUser(ctx, user))

	_, err = pkSvc.BeginRegistration(ctx, user.UID)
	r.ErrorIs(err, auth.ErrWebAuthnNotConfigured)
}

// TestPasskeyService_BeginRegistrationReturnsOptions confirms that an
// enabled service issues options + a sealed session.
func TestPasskeyService_BeginRegistrationReturnsOptions(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	pkSvc, _, user := passkeyTestUser(t)
	r.True(pkSvc.Enabled())

	resp, err := pkSvc.BeginRegistration(ctx, user.UID)
	r.NoError(err)
	r.NotNil(resp.Options)
	r.NotEmpty(resp.Session)
	// allowCredentials should be empty for a brand-new account.
	r.Empty(resp.Options.Response.CredentialExcludeList)
}

// TestPasskeyService_ListPasskeysEmpty exercises the read path with no
// stored credentials.
func TestPasskeyService_ListPasskeysEmpty(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	pkSvc, _, user := passkeyTestUser(t)

	infos, err := pkSvc.ListPasskeys(ctx, user.UID)
	r.NoError(err)
	r.Empty(infos)
}

// TestPasskeyService_RenamePasskey covers the success and
// owner-mismatch branches.
func TestPasskeyService_RenamePasskey(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	pkSvc, dbSvc, user := passkeyTestUser(t)

	row := models.NewUserPasskey(user.UID, "MacBook", []byte("cred-1"), []byte("pk"))
	r.NoError(dbSvc.CreateUserPasskey(ctx, row))

	r.NoError(pkSvc.RenamePasskey(ctx, user.UID, row.UID, "Work laptop"))

	got, err := dbSvc.GetUserPasskey(ctx, row.UID)
	r.NoError(err)
	r.Equal("Work laptop", got.Name)

	// Different user — same row UID — must fail with NotFound.
	otherUser := models.NewUser("bob@example.com")
	r.NoError(dbSvc.CreateUser(ctx, otherUser))
	r.ErrorIs(pkSvc.RenamePasskey(ctx, otherUser.UID, row.UID, "Mine"), auth.ErrPasskeyNotFound)
}

// TestPasskeyService_DeletePasskey_LastAuthMethod is the spec's
// PASSKEY_LAST_AUTH_METHOD scenario: a passkey-only user with no
// password and no other passkeys cannot remove their last credential.
func TestPasskeyService_DeletePasskey_LastAuthMethod(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	pkSvc, dbSvc, user := passkeyTestUser(t)

	// Strip the password so the passkey is the only auth method.
	r.NoError(dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{
		PasswordHash: ptrString(""),
	}))

	row := models.NewUserPasskey(user.UID, "Only key", []byte("cred-x"), []byte("pk"))
	r.NoError(dbSvc.CreateUserPasskey(ctx, row))

	err := pkSvc.DeletePasskey(ctx, user.UID, row.UID)
	r.ErrorIs(err, auth.ErrPasskeyLastAuthMethod)

	// Row must still be present.
	got, err := dbSvc.GetUserPasskey(ctx, row.UID)
	r.NoError(err)
	r.NotNil(got)
}

// TestPasskeyService_DeletePasskey_HasPassword removes is allowed when
// the user retains a password.
func TestPasskeyService_DeletePasskey_HasPassword(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	pkSvc, dbSvc, user := passkeyTestUser(t)
	// passkeyTestUser sets PasswordHash to "$plaintext$pw".

	row := models.NewUserPasskey(user.UID, "Key", []byte("cred-y"), []byte("pk"))
	r.NoError(dbSvc.CreateUserPasskey(ctx, row))

	r.NoError(pkSvc.DeletePasskey(ctx, user.UID, row.UID))
}

// TestPasskeyService_DeletePasskey_HasOtherPasskey is allowed when
// another passkey exists, even with no password.
func TestPasskeyService_DeletePasskey_HasOtherPasskey(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	pkSvc, dbSvc, user := passkeyTestUser(t)
	r.NoError(dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{PasswordHash: ptrString("")}))

	row1 := models.NewUserPasskey(user.UID, "Key 1", []byte("cred-1"), []byte("pk1"))
	row2 := models.NewUserPasskey(user.UID, "Key 2", []byte("cred-2"), []byte("pk2"))
	r.NoError(dbSvc.CreateUserPasskey(ctx, row1))
	r.NoError(dbSvc.CreateUserPasskey(ctx, row2))

	r.NoError(pkSvc.DeletePasskey(ctx, user.UID, row1.UID))
}

func ptrString(s string) *string {
	return &s
}
