package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// Spec 2026-10-31-02: adding a sign-in factor (TOTP, passkey, first password)
// needs a re-authentication, not just a session.

const reauthTestPassword = "oldpassword" // passwordResetUser's password

// signedInCtxFor is the request context RequireAuth builds for a session that
// signed in just now.
func signedInCtxFor(ctx context.Context, userUID string) context.Context {
	return signedInCtxAt(ctx, userUID, time.Now())
}

func signedInCtxAt(ctx context.Context, userUID string, at time.Time) context.Context {
	return context.WithValue(ctx, base.ContextKeyClaims,
		&Claims{UserUID: userUID, OrgSlug: "acme", AuthTime: at.Unix()})
}

func strPtr(s string) *string { return &s }

func storedTOTPSecret(t *testing.T, ctx context.Context, dbSvc db.Service, userUID string) string {
	t.Helper()

	user, err := dbSvc.GetUser(ctx, userUID)
	require.NoError(t, err)

	if user.TOTPSecret == nil {
		return ""
	}

	return *user.TOTPSecret
}

func TestSetup2FARequiresCurrentPassword(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		password *string
	}{
		{"missing", nil},
		{"empty", strPtr("")},
		{"wrong", strPtr("not-the-password")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			svc, dbSvc, ctx := setupAuthTestService(t)
			user := passwordResetUser(t, ctx, dbSvc, "totp-"+tc.name+"@example.com")

			// A fresh session is not enough for an account with a password.
			_, err := svc.Setup2FA(signedInCtxFor(ctx, user.UID), user.UID, Setup2FARequest{CurrentPassword: tc.password})
			r.ErrorIs(err, ErrInvalidCurrentPassword)
			r.Empty(storedTOTPSecret(t, ctx, dbSvc, user.UID), "no TOTP secret stored")

			// The refused attempt spent the change-password budget.
			entry, err := dbSvc.GetStateEntry(ctx, nil, changePasswordCountKeyPrefix+user.UID)
			r.NoError(err)
			r.NotNil(entry)
		})
	}
}

func TestSetup2FAWrongPasswordIsRateLimited(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	user := passwordResetUser(t, ctx, dbSvc, "totp-limit@example.com")

	for range changePasswordMaxPerUser {
		_, err := svc.Setup2FA(ctx, user.UID, Setup2FARequest{CurrentPassword: strPtr("wrong")})
		r.ErrorIs(err, ErrInvalidCurrentPassword)
	}

	// Once the shared budget is spent, even the right password is refused.
	_, err := svc.Setup2FA(ctx, user.UID, Setup2FARequest{CurrentPassword: strPtr(reauthTestPassword)})
	r.ErrorIs(err, ErrRateLimited)
	r.Empty(storedTOTPSecret(t, ctx, dbSvc, user.UID))
}

func TestSetup2FAAndConfirmWithCurrentPassword(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	user := passwordResetUser(t, ctx, dbSvc, "totp-ok@example.com")

	setup, err := svc.Setup2FA(ctx, user.UID, Setup2FARequest{CurrentPassword: strPtr(reauthTestPassword)})
	r.NoError(err)
	r.NotEmpty(setup.Secret)
	r.NotEmpty(setup.SetupToken)

	code, err := totp.GenerateCode(setup.Secret, time.Now())
	r.NoError(err)

	// The code alone does not enable 2FA: the setup token is the proof.
	_, err = svc.Confirm2FA(ctx, user.UID, Confirm2FARequest{Code: code})
	r.ErrorIs(err, ErrInvalidSetupToken)

	resp, err := svc.Confirm2FA(ctx, user.UID, Confirm2FARequest{Code: code, SetupToken: setup.SetupToken})
	r.NoError(err)
	r.NotEmpty(resp.RecoveryCodes)

	updated, err := dbSvc.GetUser(ctx, user.UID)
	r.NoError(err)
	r.True(updated.TOTPEnabled)
}

func TestConfirm2FARefusesASetupTokenForAnotherSecretOrUser(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	user := passwordResetUser(t, ctx, dbSvc, "totp-stale@example.com")
	other := passwordResetUser(t, ctx, dbSvc, "totp-other@example.com")

	first, err := svc.Setup2FA(ctx, user.UID, Setup2FARequest{CurrentPassword: strPtr(reauthTestPassword)})
	r.NoError(err)
	second, err := svc.Setup2FA(ctx, user.UID, Setup2FARequest{CurrentPassword: strPtr(reauthTestPassword)})
	r.NoError(err)

	code, err := totp.GenerateCode(second.Secret, time.Now())
	r.NoError(err)

	// The first token was for a secret that has since been replaced.
	_, err = svc.Confirm2FA(ctx, user.UID, Confirm2FARequest{Code: code, SetupToken: first.SetupToken})
	r.ErrorIs(err, ErrInvalidSetupToken)

	// Another user's token never confirms this user's setup.
	otherSetup, err := svc.Setup2FA(ctx, other.UID, Setup2FARequest{CurrentPassword: strPtr(reauthTestPassword)})
	r.NoError(err)
	_, err = svc.Confirm2FA(ctx, user.UID, Confirm2FARequest{Code: code, SetupToken: otherSetup.SetupToken})
	r.ErrorIs(err, ErrInvalidSetupToken)

	_, err = svc.Confirm2FA(ctx, user.UID, Confirm2FARequest{Code: code, SetupToken: second.SetupToken})
	r.NoError(err)
}

func TestSetup2FAPasswordlessNeedsARecentSignIn(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	user := ssoOnlyUser(t, ctx, dbSvc, "totp-sso@example.com")

	// No sign-in time at all (a session minted before the claim existed).
	_, err := svc.Setup2FA(ctx, user.UID, Setup2FARequest{})
	r.ErrorIs(err, ErrReauthenticationRequired)

	// A sign-in older than the window.
	_, err = svc.Setup2FA(signedInCtxAt(ctx, user.UID, time.Now().Add(-11*time.Minute)), user.UID, Setup2FARequest{})
	r.ErrorIs(err, ErrReauthenticationRequired)
	r.Empty(storedTOTPSecret(t, ctx, dbSvc, user.UID))

	// Positive control: signed in a minute ago.
	setup, err := svc.Setup2FA(signedInCtxAt(ctx, user.UID, time.Now().Add(-time.Minute)), user.UID, Setup2FARequest{})
	r.NoError(err)
	r.NotEmpty(setup.SetupToken)
}

func TestChangePasswordFirstPasswordNeedsARecentSignIn(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	user := ssoOnlyUser(t, ctx, dbSvc, "sso-first@example.com")
	req := ChangePasswordRequest{NewPassword: "myfirstpassword"}

	_, err := svc.ChangePassword(ctx, user.UID, "", req)
	r.ErrorIs(err, ErrReauthenticationRequired)

	_, err = svc.ChangePassword(signedInCtxAt(ctx, user.UID, time.Now().Add(-time.Hour)), user.UID, "", req)
	r.ErrorIs(err, ErrReauthenticationRequired)

	updated, err := dbSvc.GetUser(ctx, user.UID)
	r.NoError(err)
	r.Nil(updated.PasswordHash, "no password was set")

	_, err = svc.ChangePassword(signedInCtxFor(ctx, user.UID), user.UID, "", req)
	r.NoError(err)
}

func TestBeginPasskeyRegistrationRequiresReauthentication(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "http://localhost:4000")
	svc.cfg.WebAuthn.Enabled = true
	pkSvc := NewPasskeyService(svc, dbSvc)
	r.True(pkSvc.Enabled())

	user := passwordResetUser(t, ctx, dbSvc, "pk-reauth@example.com")

	_, err := pkSvc.BeginRegistration(signedInCtxFor(ctx, user.UID), user.UID, nil)
	r.ErrorIs(err, ErrInvalidCurrentPassword)
	_, err = pkSvc.BeginRegistration(ctx, user.UID, strPtr("wrong"))
	r.ErrorIs(err, ErrInvalidCurrentPassword)

	resp, err := pkSvc.BeginRegistration(ctx, user.UID, strPtr(reauthTestPassword))
	r.NoError(err)
	r.NotEmpty(resp.Session)

	sso := ssoOnlyUser(t, ctx, dbSvc, "pk-sso@example.com")
	_, err = pkSvc.BeginRegistration(ctx, sso.UID, nil)
	r.ErrorIs(err, ErrReauthenticationRequired)

	_, err = pkSvc.BeginRegistration(signedInCtxFor(ctx, sso.UID), sso.UID, nil)
	r.NoError(err)
}

func TestConfirm2FAEnqueuesTOTPEnabledNotice(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	cfg := &config.Config{
		Auth:   config.AuthConfig{JWTSecret: "test-jwt-secret", AccessTokenExpiry: time.Hour},
		Server: config.ServerConfig{BaseURL: "https://solidping.example.com"},
	}
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := NewService(dbSvc, cfg.Auth, cfg, jobs, nil)
	user := passwordResetUser(t, ctx, dbSvc, "totp-notice@example.com")
	reqCtx := signedInCtxFor(ctx, user.UID)

	setup, err := svc.Setup2FA(reqCtx, user.UID, Setup2FARequest{CurrentPassword: strPtr(reauthTestPassword)})
	r.NoError(err)

	code, err := totp.GenerateCode(setup.Secret, time.Now())
	r.NoError(err)
	_, err = svc.Confirm2FA(reqCtx, user.UID, Confirm2FARequest{Code: code, SetupToken: setup.SetupToken})
	r.NoError(err)

	listed, err := dbSvc.ListJobs(ctx, nil, 0)
	r.NoError(err)

	var notices []*models.Job

	for _, job := range listed {
		if job.Type == string(jobdef.JobTypeEmail) && job.Config["template"] == email.TemplateTOTPEnabled {
			notices = append(notices, job)
		}
	}

	r.Len(notices, 1, "exactly one totp-enabled notice")
	r.Equal([]any{user.Email}, notices[0].Config["to"])

	data, ok := notices[0].Config["templateData"].(map[string]any)
	r.True(ok)
	r.Equal("https://solidping.example.com"+config.DashboardBasePath+"/orgs/acme/account/security", data["SecurityURL"])
}

// postFactorRoute calls a factor-adding handler with the given claims.
func postFactorRoute(
	t *testing.T, ctx context.Context, claims *Claims, path, body string,
	serve func(http.ResponseWriter, *http.Request) error,
) *httptest.ResponseRecorder {
	t.Helper()

	reqCtx := context.WithValue(ctx, base.ContextKeyClaims, claims)
	httpReq := httptest.NewRequestWithContext(reqCtx, http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	require.NoError(t, serve(rec, httpReq))

	return rec
}

func TestFactorRoutesHTTPErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "http://localhost:4000")
	svc.cfg.WebAuthn.Enabled = true
	handler := NewHandler(svc, &config.Config{})
	pkHandler := NewPasskeyHandler(NewPasskeyService(svc, dbSvc), base.NewHandlerBase(&config.Config{}))

	user := passwordResetUser(t, ctx, dbSvc, "http-reauth@example.com")
	claims := &Claims{UserUID: user.UID, AuthTime: time.Now().Unix()}

	// No body at all: the password is missing.
	rec := postFactorRoute(t, ctx, claims, "/api/v1/auth/2fa/setup", "", handler.Setup2FA)
	r.Equal(http.StatusUnauthorized, rec.Code)
	r.Equal(string(base.ErrorCodeInvalidCurrentPassword), decodeErrorCode(t, rec))

	rec = postFactorRoute(t, ctx, claims, "/api/v1/auth/passkeys/register/begin", `{}`, pkHandler.RegisterBegin)
	r.Equal(http.StatusUnauthorized, rec.Code)
	r.Equal(string(base.ErrorCodeInvalidCurrentPassword), decodeErrorCode(t, rec))

	// Positive control.
	rec = postFactorRoute(t, ctx, claims, "/api/v1/auth/2fa/setup",
		`{"currentPassword":"`+reauthTestPassword+`"}`, handler.Setup2FA)
	r.Equal(http.StatusOK, rec.Code)

	rec = postFactorRoute(t, ctx, claims, "/api/v1/auth/passkeys/register/begin",
		`{"currentPassword":"`+reauthTestPassword+`"}`, pkHandler.RegisterBegin)
	r.Equal(http.StatusOK, rec.Code)

	// Confirm without its setup token.
	rec = postFactorRoute(t, ctx, claims, "/api/v1/auth/2fa/confirm", `{"code":"123456"}`, handler.Confirm2FA)
	r.Equal(http.StatusBadRequest, rec.Code)
	r.Equal(string(base.ErrorCodeInvalidSetupToken), decodeErrorCode(t, rec))

	// Burn the budget, then the cap answers 429.
	for range changePasswordMaxPerUser {
		postFactorRoute(t, ctx, claims, "/api/v1/auth/2fa/setup", `{"currentPassword":"x"}`, handler.Setup2FA)
	}

	rec = postFactorRoute(t, ctx, claims, "/api/v1/auth/2fa/setup", `{"currentPassword":"x"}`, handler.Setup2FA)
	r.Equal(http.StatusTooManyRequests, rec.Code)

	// A passwordless account with a stale session is sent to sign in again.
	sso := ssoOnlyUser(t, ctx, dbSvc, "http-sso@example.com")
	stale := &Claims{UserUID: sso.UID, AuthTime: time.Now().Add(-time.Hour).Unix()}

	for _, route := range []struct {
		path  string
		serve func(http.ResponseWriter, *http.Request) error
	}{
		{"/api/v1/auth/2fa/setup", handler.Setup2FA},
		{"/api/v1/auth/passkeys/register/begin", pkHandler.RegisterBegin},
	} {
		rec = postFactorRoute(t, ctx, stale, route.path, "", route.serve)
		r.Equal(http.StatusForbidden, rec.Code, route.path)
		r.Equal(string(base.ErrorCodeReauthenticationRequired), decodeErrorCode(t, rec), route.path)
	}

	rec = postChangePassword(t, ctx, handler, stale, `{"newPassword":"myfirstpassword"}`)
	r.Equal(http.StatusForbidden, rec.Code)
	r.Equal(string(base.ErrorCodeReauthenticationRequired), decodeErrorCode(t, rec))
}

// TestSessionCarriesTheSignInTime pins where Claims.AuthTime comes from: a
// login stamps it, a refresh copies the stored sign-in time (never its own),
// and an org switch keeps the caller's.
func TestSessionCarriesTheSignInTime(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	changePasswordFixture(t, ctx, dbSvc, "authtime", "authtime@example.com")
	r.NoError(dbSvc.CreateOrganization(ctx, models.NewOrganization("authtime-2", "authtime-2")))

	before := time.Now().Unix()
	login, err := svc.Login(ctx, "authtime", "authtime@example.com", "testpass1234", Context{})
	r.NoError(err)

	claims, err := svc.ValidateToken(ctx, login.AccessToken)
	r.NoError(err)
	r.GreaterOrEqual(claims.AuthTime, before)

	// Age the stored sign-in time: a refresh must report it, not "now".
	token, err := dbSvc.GetUserTokenByToken(ctx, login.RefreshToken)
	r.NoError(err)

	old := time.Now().Add(-time.Hour).Unix()
	props := token.Properties
	props[keySignedInAt] = old
	r.NoError(dbSvc.UpdateUserToken(ctx, token.UID, models.UserTokenUpdate{Properties: &props}))

	refreshed, err := svc.Refresh(ctx, login.RefreshToken)
	r.NoError(err)
	refreshedClaims, err := svc.ValidateToken(ctx, refreshed.AccessToken)
	r.NoError(err)
	r.Equal(old, refreshedClaims.AuthTime)

	// An org switch keeps the caller's sign-in time.
	user, err := dbSvc.GetUserByEmail(ctx, "authtime@example.com")
	r.NoError(err)
	org2, err := dbSvc.GetOrganizationBySlug(ctx, "authtime-2")
	r.NoError(err)
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org2.UID, user.UID, models.MemberRoleAdmin)))

	switched, err := svc.SwitchOrg(context.WithValue(ctx, base.ContextKeyClaims, refreshedClaims),
		user.UID, "authtime-2", Context{})
	r.NoError(err)
	switchedClaims, err := svc.ValidateToken(ctx, switched.AccessToken)
	r.NoError(err)
	r.Equal(old, switchedClaims.AuthTime)

	// And a refresh of the switched session still reports it.
	again, err := svc.Refresh(ctx, switched.RefreshToken)
	r.NoError(err)
	againClaims, err := svc.ValidateToken(ctx, again.AccessToken)
	r.NoError(err)
	r.Equal(old, againClaims.AuthTime)
}
