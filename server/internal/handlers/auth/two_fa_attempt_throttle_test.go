package auth

import (
	"context"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portTwoFAAttemptPG is distinct from every other embedded-Postgres port
// claimed in the repo (see the port-numbering note in
// provider_links_postgres_test.go).
const portTwoFAAttemptPG = 15563

// TestTwoFAAttemptThrottle_SQLite is the SQLite half of the dual-engine
// coverage for the per-user 2FA attempt limiter (spec
// 2026-09-25-29-totp-attempt-throttle): both TOTP and recovery-code
// verification share one counter, backed by the same DB state-entry
// mechanism as changePasswordCountKeyPrefix.
//
//nolint:tparallel // sub-tests share the suite's dbSvc; see runTwoFAAttemptThrottleSuite
func TestTwoFAAttemptThrottle_SQLite(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))

	t.Cleanup(func() { _ = dbSvc.Close() })

	runTwoFAAttemptThrottleSuite(t, dbSvc)
}

// TestTwoFAAttemptThrottle_Postgres is the real-engine twin: the counter
// lives in the `state_entries` table, whose expiry filter
// (`expires_at > NOW()`) is written once per dialect, so proving the window
// behavior on SQLite alone would prove only half of it.
//
//nolint:tparallel // one embedded PG instance shared by every sub-test
func TestTwoFAAttemptThrottle_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portTwoFAAttemptPG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)

		return
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)

		return
	}

	runTwoFAAttemptThrottleSuite(t, dbSvc)
}

// runTwoFAAttemptThrottleSuite exercises the throttle behavior against
// whichever engine the caller wired up. Each case creates its own
// organization and user (unique slugs/emails), so sharing one dbSvc across
// them — required for the embedded-Postgres twin — never collides.
func runTwoFAAttemptThrottleSuite(t *testing.T, dbSvc db.Service) {
	t.Helper()

	t.Run("5 wrong codes then throttled, even a correct code afterward", func(t *testing.T) {
		testTOTPFiveWrongThenThrottled(t, dbSvc)
	})
	t.Run("correct code within budget succeeds and resets the counter", func(t *testing.T) {
		testTOTPSuccessResetsCounter(t, dbSvc)
	})
	t.Run("recovery codes share the counter with TOTP", func(t *testing.T) {
		testRecoverySharesCounterWithTOTP(t, dbSvc)
	})
	t.Run("a fresh temp token does not reset the counter", func(t *testing.T) {
		testTempTokenRefreshDoesNotResetCounter(t, dbSvc)
	})
	t.Run("other users are unaffected", func(t *testing.T) {
		testOtherUsersUnaffectedByThrottle(t, dbSvc)
	})
	t.Run("the counter clears once its window has elapsed", func(t *testing.T) {
		testTwoFAWindowExpiryClearsThrottle(t, dbSvc)
	})
}

// newTwoFAThrottleFixture creates an organization plus a TOTP-enabled admin
// member and returns everything a test needs to drive Verify2FA/Recovery2FA
// directly: the service, the org, the user, and the raw TOTP secret (to mint
// valid/wrong codes on demand).
//
//nolint:revive // ctx-second is fine in test helpers; matches existing helpers in this file
func newTwoFAThrottleFixture(
	t *testing.T, dbSvc db.Service, ctx context.Context, slugSuffix string,
) (*Service, *models.Organization, *models.User, string) {
	t.Helper()
	r := require.New(t)

	cfg := &config.Config{
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
		Server: config.ServerConfig{BaseURL: "http://localhost:4000"},
	}

	org := models.NewOrganization("acme-2fa-"+slugSuffix, "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("2fa-" + slugSuffix + "@acme.com")
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	key, err := totp.Generate(totp.GenerateOpts{Issuer: totpTestIssuer, AccountName: user.Email})
	r.NoError(err)

	secret := key.Secret()
	enabled := true
	r.NoError(dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{
		TOTPSecret:  &secret,
		TOTPEnabled: &enabled,
	}))

	return NewService(dbSvc, cfg.Auth, cfg, nil, nil), org, user, secret
}

// mintTwoFATempToken is a small wrapper so call sites read as "give me a
// temp token for this user" without repeating the claim shape everywhere.
func mintTwoFATempToken(t *testing.T, svc *Service, org *models.Organization, user *models.User) string {
	t.Helper()

	tempToken, err := svc.generate2FATempToken(user.UID, org.Slug, string(models.MemberRoleAdmin), AuthMethodLDAP)
	require.NoError(t, err)

	return tempToken
}

// wrongTOTPCode returns a 6-digit code that is guaranteed NOT to validate
// against secret right now, by flipping the last digit of the real one —
// deterministic, unlike picking an arbitrary fixed string that could
// coincidentally match.
func wrongTOTPCode(t *testing.T, secret string) string {
	t.Helper()

	valid, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)

	digits := []byte(valid)
	last := len(digits) - 1

	if digits[last] == '0' {
		digits[last] = '1'
	} else {
		digits[last] = '0'
	}

	return string(digits)
}

// testTOTPFiveWrongThenThrottled proves the core cap: the limiter consumes
// the per-user budget on every attempt (right or wrong), so the 5 wrong
// codes here each get the ordinary invalid-code answer, the 6th is throttled
// outright, and — the load-bearing bit — a *correct* code no longer helps
// once the cap is reached, because the limiter guards attempts, not
// outcomes (mirrors TestChangePasswordRateLimit).
func testTOTPFiveWrongThenThrottled(t *testing.T, dbSvc db.Service) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	svc, org, user, secret := newTwoFAThrottleFixture(t, dbSvc, ctx, "fivewrong")
	tempToken := mintTwoFATempToken(t, svc, org, user)
	wrong := wrongTOTPCode(t, secret)

	for i := 0; i < twoFAAttemptMaxPerUser; i++ {
		_, verifyErr := svc.Verify2FA(ctx, tempToken, wrong, Context{})
		r.ErrorIsf(verifyErr, ErrInvalid2FACode, "attempt %d must still be a plain wrong-code answer", i+1)
	}

	// 6th attempt (still a wrong code): throttled before the code is checked.
	_, err := svc.Verify2FA(ctx, tempToken, wrong, Context{})
	r.ErrorIs(err, ErrRateLimited)

	// Even the *correct* code is refused once the cap is reached.
	validCode, err := totp.GenerateCode(secret, time.Now())
	r.NoError(err)

	_, err = svc.Verify2FA(ctx, tempToken, validCode, Context{})
	r.ErrorIs(err, ErrRateLimited)
}

// testTOTPSuccessResetsCounter proves a legitimate user is never punished for
// one earlier typo: after one wrong attempt, a correct one succeeds and
// wipes the counter entirely.
func testTOTPSuccessResetsCounter(t *testing.T, dbSvc db.Service) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	svc, org, user, secret := newTwoFAThrottleFixture(t, dbSvc, ctx, "reset")
	tempToken := mintTwoFATempToken(t, svc, org, user)
	wrong := wrongTOTPCode(t, secret)

	_, err := svc.Verify2FA(ctx, tempToken, wrong, Context{})
	r.ErrorIs(err, ErrInvalid2FACode)

	// A fresh temp token, same 15-minute window: only 1 of the 5-attempt
	// budget has been spent, so the correct code still succeeds.
	tempToken2 := mintTwoFATempToken(t, svc, org, user)

	validCode, err := totp.GenerateCode(secret, time.Now())
	r.NoError(err)

	_, err = svc.Verify2FA(ctx, tempToken2, validCode, Context{})
	r.NoError(err)

	entry, err := dbSvc.GetStateEntry(ctx, nil, twoFAAttemptCountKeyPrefix+user.UID)
	r.NoError(err)
	r.Nil(entry, "a successful verification must clear the attempt counter")
}

// testRecoverySharesCounterWithTOTP proves TOTP and recovery-code
// verification spend the same per-user budget: recovery codes are fewer
// valid values than TOTP, so they must not get a separate allowance.
func testRecoverySharesCounterWithTOTP(t *testing.T, dbSvc db.Service) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	svc, org, user, secret := newTwoFAThrottleFixture(t, dbSvc, ctx, "shared")

	codes, err := generateRecoveryCodes()
	r.NoError(err)
	r.NoError(dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{TOTPRecoveryCodes: &codes}))

	tempToken := mintTwoFATempToken(t, svc, org, user)
	wrong := wrongTOTPCode(t, secret)

	// 4 wrong TOTP attempts.
	for i := 0; i < twoFAAttemptMaxPerUser-1; i++ {
		_, verifyErr := svc.Verify2FA(ctx, tempToken, wrong, Context{})
		r.ErrorIs(verifyErr, ErrInvalid2FACode)
	}

	// 5th attempt, via a wrong recovery code, still spends the same budget.
	_, err = svc.Recovery2FA(ctx, tempToken, "not-a-real-code", Context{})
	r.ErrorIs(err, ErrInvalidRecoveryCode)

	// 6th attempt — a genuinely valid recovery code this time — is throttled:
	// proof the TOTP and recovery-code paths share one counter, not two.
	_, err = svc.Recovery2FA(ctx, tempToken, codes[0], Context{})
	r.ErrorIs(err, ErrRateLimited)
}

// testTempTokenRefreshDoesNotResetCounter proves the counter is keyed by
// user, not by temp-token ID: re-requesting a temp token (as a real user
// would by re-submitting their password) must not buy a fresh budget.
func testTempTokenRefreshDoesNotResetCounter(t *testing.T, dbSvc db.Service) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	svc, org, user, secret := newTwoFAThrottleFixture(t, dbSvc, ctx, "refresh")
	tempToken1 := mintTwoFATempToken(t, svc, org, user)
	wrong := wrongTOTPCode(t, secret)

	for i := 0; i < twoFAAttemptMaxPerUser; i++ {
		_, verifyErr := svc.Verify2FA(ctx, tempToken1, wrong, Context{})
		r.ErrorIs(verifyErr, ErrInvalid2FACode)
	}

	// A brand-new temp token must not reset the budget.
	tempToken2 := mintTwoFATempToken(t, svc, org, user)

	_, err := svc.Verify2FA(ctx, tempToken2, wrong, Context{})
	r.ErrorIs(err, ErrRateLimited)
}

// testOtherUsersUnaffectedByThrottle proves the limiter is scoped per user,
// not per IP or process-wide: throttling one account leaves a second
// account, verified through the very same Service, untouched.
func testOtherUsersUnaffectedByThrottle(t *testing.T, dbSvc db.Service) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	svc, orgA, userA, secretA := newTwoFAThrottleFixture(t, dbSvc, ctx, "victima")
	_, orgB, userB, secretB := newTwoFAThrottleFixture(t, dbSvc, ctx, "victimb")

	tokenA := mintTwoFATempToken(t, svc, orgA, userA)
	wrongA := wrongTOTPCode(t, secretA)

	for i := 0; i < twoFAAttemptMaxPerUser; i++ {
		_, verifyErr := svc.Verify2FA(ctx, tokenA, wrongA, Context{})
		r.ErrorIs(verifyErr, ErrInvalid2FACode)
	}

	_, err := svc.Verify2FA(ctx, tokenA, wrongA, Context{})
	r.ErrorIs(err, ErrRateLimited, "user A must be throttled")

	// User B, resolved through the same Service, is untouched.
	tokenB := mintTwoFATempToken(t, svc, orgB, userB)

	validB, err := totp.GenerateCode(secretB, time.Now())
	r.NoError(err)

	_, err = svc.Verify2FA(ctx, tokenB, validB, Context{})
	r.NoError(err, "user B's own budget must be untouched by user A's throttling")
}

// testTwoFAWindowExpiryClearsThrottle proves the throttle is bounded by the
// configured window rather than permanent. There is no clock seam on this
// Service (unlike usernotifications' s.now()), so — following the same
// direct-state-entry technique the password-reset tests already use to seed
// entries the API itself cannot produce — the window's passing is simulated
// by writing the counter's state entry with an already-past expiry.
func testTwoFAWindowExpiryClearsThrottle(t *testing.T, dbSvc db.Service) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	svc, org, user, secret := newTwoFAThrottleFixture(t, dbSvc, ctx, "expiry")
	tempToken := mintTwoFATempToken(t, svc, org, user)
	wrong := wrongTOTPCode(t, secret)

	for i := 0; i < twoFAAttemptMaxPerUser; i++ {
		_, verifyErr := svc.Verify2FA(ctx, tempToken, wrong, Context{})
		r.ErrorIs(verifyErr, ErrInvalid2FACode)
	}

	_, err := svc.Verify2FA(ctx, tempToken, wrong, Context{})
	r.ErrorIs(err, ErrRateLimited, "cap must be reached before simulating the window passing")

	// Simulate the 15-minute window having elapsed.
	expired := -time.Second
	r.NoError(dbSvc.SetStateEntry(ctx, nil, twoFAAttemptCountKeyPrefix+user.UID,
		&models.JSONMap{stateValueKeyCount: twoFAAttemptMaxPerUser}, &expired))

	tempToken2 := mintTwoFATempToken(t, svc, org, user)

	validCode, err := totp.GenerateCode(secret, time.Now())
	r.NoError(err)

	_, err = svc.Verify2FA(ctx, tempToken2, validCode, Context{})
	r.NoError(err, "once the window has passed, the counter is gone and a correct code succeeds again")
}
