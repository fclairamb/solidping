package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

const tokenPurposePassword = "correct-horse-battery"

type tokenPurposeFixture struct {
	router         *httpx.Router
	passkeySession string
	tempToken      string
	accessToken    string
}

// setupTokenPurposeFixture mints, through the public service APIs, the two
// non-bearer tokens an attacker can obtain (the passkey login-begin session
// for an email with a passkey, and the 2FA temp token from a password login)
// plus a regular session, and mounts RequireAuth on a real router.
func setupTokenPurposeFixture(t *testing.T) *tokenPurposeFixture {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbService.Initialize(ctx))
	t.Cleanup(func() { _ = dbService.Close() })

	cfg := &config.Config{
		Server: config.ServerConfig{BaseURL: "https://solidping.test"},
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
			WebAuthn:           config.WebAuthnConfig{Enabled: true},
		},
	}

	authSvc := auth.NewService(dbService, cfg.Auth, cfg, nil, nil)
	pkSvc := auth.NewPasskeyService(authSvc, dbService)
	r.True(pkSvc.Enabled())

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbService.CreateOrganization(ctx, org))

	hash, err := passwords.Hash(tokenPurposePassword)
	r.NoError(err)

	newMember := func(email string) *models.User {
		user := models.NewUser(email)
		user.PasswordHash = &hash
		r.NoError(dbService.CreateUser(ctx, user))
		r.NoError(dbService.CreateOrganizationMember(ctx,
			models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

		return user
	}

	alice := newMember("alice@acme.com")
	passkey := models.NewUserPasskey(alice.UID, "Laptop", []byte("cred-1"), []byte("pub"))
	r.NoError(dbService.CreateUserPasskey(ctx, passkey))

	bob := newMember("bob@acme.com")
	secret := "JBSWY3DPEHPK3PXP"
	enabled := true
	r.NoError(dbService.UpdateUser(ctx, bob.UID, &models.UserUpdate{TOTPSecret: &secret, TOTPEnabled: &enabled}))

	f := &tokenPurposeFixture{router: httpx.New()}

	begin, err := pkSvc.BeginLogin(ctx, alice.Email)
	r.NoError(err)
	f.passkeySession = begin.Session

	twoFA, err := authSvc.Login(ctx, "acme", bob.Email, tokenPurposePassword, auth.Context{})
	r.NoError(err)
	r.True(twoFA.Requires2FA)
	r.NotEmpty(twoFA.TempToken)
	f.tempToken = twoFA.TempToken

	login, err := authSvc.Login(ctx, "acme", alice.Email, tokenPurposePassword, auth.Context{})
	r.NoError(err)
	r.False(login.Requires2FA)
	r.NotEmpty(login.AccessToken)
	f.accessToken = login.AccessToken

	reached := func(w http.ResponseWriter, _ *http.Request) error {
		w.WriteHeader(http.StatusOK)

		return nil
	}

	authed := f.router.Use(middleware.NewAuthMiddleware(authSvc, dbService, cfg).RequireAuth)
	authed.GET("/api/v1/auth/me", reached)
	authed.POST("/api/v1/auth/switch-org", reached)
	authed.GET("/api/v1/orgs/:org/checks", reached)

	return f
}

func (f *tokenPurposeFixture) do(t *testing.T, method, path, token string) (int, string) {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		return rec.Code, ""
	}

	return rec.Code, decodeErrorCode(t, rec)
}

func TestRequireAuthRefusesNonBearerTokens(t *testing.T) {
	t.Parallel()

	f := setupTokenPurposeFixture(t)

	cases := []struct {
		name, method, path string
		token              func() string
		wantStatus         int
	}{
		{
			"passkey login session on /auth/me", http.MethodGet, "/api/v1/auth/me",
			func() string { return f.passkeySession }, http.StatusUnauthorized,
		},
		{
			"passkey login session on switch-org", http.MethodPost, "/api/v1/auth/switch-org",
			func() string { return f.passkeySession }, http.StatusUnauthorized,
		},
		{
			"2FA temp token on an org route", http.MethodGet, "/api/v1/orgs/acme/checks",
			func() string { return f.tempToken }, http.StatusUnauthorized,
		},
		{
			"2FA temp token on switch-org", http.MethodPost, "/api/v1/auth/switch-org",
			func() string { return f.tempToken }, http.StatusUnauthorized,
		},
		// Positive controls: a password-login session is a bearer.
		{
			"session on /auth/me", http.MethodGet, "/api/v1/auth/me",
			func() string { return f.accessToken }, http.StatusOK,
		},
		{
			"session on an org route", http.MethodGet, "/api/v1/orgs/acme/checks",
			func() string { return f.accessToken }, http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			status, code := f.do(t, tc.method, tc.path, tc.token())
			require.Equal(t, tc.wantStatus, status)

			if tc.wantStatus == http.StatusUnauthorized {
				require.Equal(t, string(base.ErrorCodeInvalidToken), code)
			}
		})
	}
}
