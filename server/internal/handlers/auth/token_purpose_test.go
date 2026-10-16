package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

const tokenPurposeTestSecret = "token-purpose-test-secret"

type tokenPurposeFixture struct {
	svc   *Service
	pkSvc *PasskeyService
	user  *models.User
}

// newTokenPurposeFixture wires an auth service with WebAuthn enabled and a
// user owning one passkey, so BeginLogin puts the user's UID in the session.
func newTokenPurposeFixture(t *testing.T) *tokenPurposeFixture {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	user := models.NewUser("alice@acme.com")
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateUserPasskey(ctx, models.NewUserPasskey(user.UID, "Laptop", []byte("cred-1"), []byte("pub"))))

	cfg := &config.Config{
		Auth: config.AuthConfig{
			JWTSecret:         tokenPurposeTestSecret,
			AccessTokenExpiry: time.Hour,
			WebAuthn:          config.WebAuthnConfig{Enabled: true},
		},
		Server: config.ServerConfig{BaseURL: "https://acme.com"},
	}
	svc := NewService(dbSvc, cfg.Auth, cfg, nil, nil)

	return &tokenPurposeFixture{svc: svc, pkSvc: NewPasskeyService(svc, dbSvc), user: user}
}

// rawSign signs claims with the raw bearer secret, the way a replica running
// the previous version minted every JWT.
func rawSign(t *testing.T, method jwt.SigningMethod, claims jwt.Claims) string {
	t.Helper()

	signed, err := jwt.NewWithClaims(method, claims).SignedString([]byte(tokenPurposeTestSecret))
	require.NoError(t, err)

	return signed
}

func validRegistered(withIssuer bool) jwt.RegisteredClaims {
	now := time.Now()
	rc := jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
		IssuedAt:  jwt.NewNumericDate(now),
	}

	if withIssuer {
		rc.Issuer = jwtIssuer
	}

	return rc
}

func TestValidateTokenRefusesNonBearerTokens(t *testing.T) {
	t.Parallel()

	f := newTokenPurposeFixture(t)
	ctx := t.Context()
	uid := f.user.UID

	cases := []struct {
		name  string
		mint  func(t *testing.T) string
		valid bool
	}{
		{"2fa temp token", func(t *testing.T) string {
			t.Helper()
			tok, err := f.svc.generate2FATempToken(uid, "acme", string(models.MemberRoleAdmin), AuthMethodPassword)
			require.NoError(t, err)

			return tok
		}, false},
		{"passkey login session", func(t *testing.T) string {
			t.Helper()
			resp, err := f.pkSvc.BeginLogin(ctx, f.user.Email)
			require.NoError(t, err)

			return resp.Session
		}, false},
		{"passkey register session", func(t *testing.T) string {
			t.Helper()
			resp, err := f.pkSvc.BeginRegistration(ctx, uid)
			require.NoError(t, err)

			return resp.Session
		}, false},
		{"legacy 2fa temp token on raw secret", func(t *testing.T) string {
			t.Helper()

			return rawSign(t, jwt.SigningMethodHS256, &TwoFAClaims{
				UserUID: uid, OrgSlug: "acme", Role: string(models.MemberRoleAdmin), Purpose: twoFAPurpose,
				RegisteredClaims: validRegistered(true),
			})
		}, false},
		{"legacy passkey login session on raw secret", func(t *testing.T) string {
			t.Helper()

			return rawSign(t, jwt.SigningMethodHS256, &passkeySessionClaims{
				Purpose: passkeySessionPurposeLogin, UserUID: uid, SessionRaw: "{}",
				RegisteredClaims: validRegistered(false),
			})
		}, false},
		{"legacy passkey session with issuer on raw secret", func(t *testing.T) string {
			t.Helper()

			return rawSign(t, jwt.SigningMethodHS256, &passkeySessionClaims{
				Purpose: passkeySessionPurposeLogin, UserUID: uid, SessionRaw: "{}",
				RegisteredClaims: validRegistered(true),
			})
		}, false},
		{"claims without issuer", func(t *testing.T) string {
			t.Helper()

			return rawSign(t, jwt.SigningMethodHS256, &Claims{
				UserUID: uid, OrgSlug: "acme", RegisteredClaims: validRegistered(false),
			})
		}, false},
		{"claims without expiry", func(t *testing.T) string {
			t.Helper()

			return rawSign(t, jwt.SigningMethodHS256, &Claims{
				UserUID: uid, OrgSlug: "acme", RegisteredClaims: jwt.RegisteredClaims{Issuer: jwtIssuer},
			})
		}, false},
		{"claims signed with HS512", func(t *testing.T) string {
			t.Helper()

			return rawSign(t, jwt.SigningMethodHS512, &Claims{
				UserUID: uid, OrgSlug: "acme", RegisteredClaims: validRegistered(true),
			})
		}, false},
		{"access token", func(t *testing.T) string {
			t.Helper()
			tok, err := f.svc.generateAccessToken(uid, "acme", string(models.MemberRoleAdmin), "", false)
			require.NoError(t, err)

			return tok
		}, true},
		{"impersonation token", func(t *testing.T) string {
			t.Helper()
			now := time.Now()
			tok, err := f.svc.generateImpersonationToken(
				f.user, "acme", string(models.MemberRoleUser), "admin-uid", now, now.Add(time.Minute))
			require.NoError(t, err)

			return tok
		}, true},
		{"mcp access token", func(t *testing.T) string {
			t.Helper()
			tok, err := f.svc.GenerateMCPAccessToken(ctx, uid, "acme", []string{"mcp"}, "https://acme.com/mcp", time.Minute, "", "")
			require.NoError(t, err)

			return tok
		}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			claims, err := f.svc.ValidateToken(ctx, tc.mint(t))
			if !tc.valid {
				require.ErrorIs(t, err, ErrInvalidToken)
				require.Nil(t, claims)

				return
			}

			require.NoError(t, err)
			require.Equal(t, uid, claims.UserUID)
			require.Equal(t, "acme", claims.OrgSlug)
		})
	}
}

func TestNonBearerTokensStayWithinTheirPurpose(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newTokenPurposeFixture(t)
	ctx := t.Context()

	twoFA, err := f.svc.generate2FATempToken(f.user.UID, "acme", string(models.MemberRoleAdmin), AuthMethodPassword)
	r.NoError(err)

	login, err := f.pkSvc.BeginLogin(ctx, f.user.Email)
	r.NoError(err)

	register, err := f.pkSvc.BeginRegistration(ctx, f.user.UID)
	r.NoError(err)

	access, err := f.svc.generateAccessToken(f.user.UID, "acme", string(models.MemberRoleAdmin), "", false)
	r.NoError(err)

	// Positive controls: each verifier accepts its own token.
	twoFAClaims, err := f.svc.validate2FATempToken(twoFA)
	r.NoError(err)
	r.Equal(f.user.UID, twoFAClaims.UserUID)

	loginClaims, _, err := f.svc.unsealPasskeySession(login.Session, passkeySessionPurposeLogin)
	r.NoError(err)
	r.Equal(f.user.UID, loginClaims.UserUID)

	_, _, err = f.svc.unsealPasskeySession(register.Session, passkeySessionPurposeRegister)
	r.NoError(err)

	// Cross-purpose and bearer tokens are refused.
	for _, tok := range []string{login.Session, register.Session, access} {
		_, err = f.svc.validate2FATempToken(tok)
		r.ErrorIs(err, ErrInvalidToken)
	}

	for _, tok := range []string{twoFA, register.Session, access} {
		_, _, err = f.svc.unsealPasskeySession(tok, passkeySessionPurposeLogin)
		r.ErrorIs(err, errPasskeySessionInvalid)
	}

	for _, tok := range []string{twoFA, login.Session, access} {
		_, _, err = f.svc.unsealPasskeySession(tok, passkeySessionPurposeRegister)
		r.ErrorIs(err, errPasskeySessionInvalid)
	}

	// A passkey session signed with HS512 under the right derived key is
	// refused: unsealPasskeySession pins HS256.
	key, err := f.svc.purposeKey(string(passkeySessionPurposeLogin))
	r.NoError(err)

	hs512, err := jwt.NewWithClaims(jwt.SigningMethodHS512, &passkeySessionClaims{
		Purpose: passkeySessionPurposeLogin, SessionRaw: "{}", RegisteredClaims: validRegistered(false),
	}).SignedString(key)
	r.NoError(err)

	_, _, err = f.svc.unsealPasskeySession(hs512, passkeySessionPurposeLogin)
	r.ErrorIs(err, errPasskeySessionInvalid)

	_, err = f.svc.purposeKey("")
	r.Error(err)
}

// TestPurposeKeyFollowsLiveSecret pins that keys are derived from fullCfg at
// call time: the system-config overlay rewrites the secret after NewService.
func TestPurposeKeyFollowsLiveSecret(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newTokenPurposeFixture(t)

	tok, err := f.svc.generate2FATempToken(f.user.UID, "acme", string(models.MemberRoleAdmin), AuthMethodPassword)
	r.NoError(err)

	f.svc.fullCfg.Auth.JWTSecret = "rotated-secret"

	_, err = f.svc.validate2FATempToken(tok)
	r.ErrorIs(err, ErrInvalidToken)
}
