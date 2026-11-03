package auth

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// Spec 2026-11-03-03: a TOTP code is accepted once (RFC 6238 §5.2), and
// turning 2FA off sends a notice.

func newTOTPReplayFixture(t *testing.T, suffix string) (*Service, *models.Organization, *models.User, string) {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return newTwoFAThrottleFixture(t, dbSvc, ctx, suffix)
}

func TestTOTPReplayRefusedByVerifyAndDisable(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org, user, secret := newTOTPReplayFixture(t, "replay")

	code, err := totp.GenerateCode(secret, time.Now())
	r.NoError(err)

	_, err = svc.Verify2FA(ctx, mintTwoFATempToken(t, svc, org, user), code, Context{})
	r.NoError(err, "first use is accepted")

	_, err = svc.Verify2FA(ctx, mintTwoFATempToken(t, svc, org, user), code, Context{})
	r.ErrorIs(err, ErrInvalid2FACode, "the same code with a new temp token is refused")

	r.ErrorIs(svc.Disable2FA(ctx, user.UID, code), ErrInvalid2FACode,
		"the code used to sign in cannot remove the factor")

	stored, err := svc.db.GetUser(ctx, user.UID)
	r.NoError(err)
	r.True(stored.TOTPEnabled, "2FA is still on")
}

func TestTOTPReplayCountsAsFailedAttempt(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org, user, secret := newTOTPReplayFixture(t, "rp-budget")

	code, err := totp.GenerateCode(secret, time.Now())
	r.NoError(err)

	_, err = svc.Verify2FA(ctx, mintTwoFATempToken(t, svc, org, user), code, Context{})
	r.NoError(err)

	for range twoFAAttemptMaxPerUser {
		_, err = svc.Verify2FA(ctx, mintTwoFATempToken(t, svc, org, user), code, Context{})
		r.ErrorIs(err, ErrInvalid2FACode)
	}

	next, err := totp.GenerateCode(secret, time.Now().Add(totpPeriod*time.Second))
	r.NoError(err)

	_, err = svc.Verify2FA(ctx, mintTwoFATempToken(t, svc, org, user), next, Context{})
	r.ErrorIs(err, ErrRateLimited, "refused replays spent the attempt budget")
}

func TestTOTPConcurrentVerifyOneWins(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org, user, secret := newTOTPReplayFixture(t, "rp-race")

	code, err := totp.GenerateCode(secret, time.Now())
	r.NoError(err)

	const parallel = 4 // below the attempt budget

	tokens := make([]string, parallel)
	for i := range tokens {
		tokens[i] = mintTwoFATempToken(t, svc, org, user)
	}

	var (
		wg       sync.WaitGroup
		accepted atomic.Int32
		refused  atomic.Int32
	)

	for _, token := range tokens {
		wg.Go(func() {
			_, verifyErr := svc.Verify2FA(ctx, token, code, Context{})

			switch {
			case verifyErr == nil:
				accepted.Add(1)
			case verifyErr == ErrInvalid2FACode: //nolint:errorlint // sentinel returned as is
				refused.Add(1)
			}
		})
	}

	wg.Wait()
	r.Equal(int32(1), accepted.Load(), "exactly one concurrent use succeeds")
	r.Equal(int32(parallel-1), refused.Load())
}

func TestTOTPNextCodeAcceptedAfterRefusedReplay(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org, user, secret := newTOTPReplayFixture(t, "rp-next")

	code, err := totp.GenerateCode(secret, time.Now())
	r.NoError(err)

	_, err = svc.Verify2FA(ctx, mintTwoFATempToken(t, svc, org, user), code, Context{})
	r.NoError(err)

	_, err = svc.Verify2FA(ctx, mintTwoFATempToken(t, svc, org, user), code, Context{})
	r.ErrorIs(err, ErrInvalid2FACode)

	next, err := totp.GenerateCode(secret, time.Now().Add(totpPeriod*time.Second))
	r.NoError(err)

	_, err = svc.Verify2FA(ctx, mintTwoFATempToken(t, svc, org, user), next, Context{})
	r.NoError(err, "the next time step is accepted")
}

func TestTOTPConfirmCodeCannotSignIn(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org, user, _ := newTOTPReplayFixture(t, "rp-confirm")

	// Start over from a pending secret, as Setup2FA leaves it.
	disabled := false
	r.NoError(svc.db.UpdateUser(ctx, user.UID, &models.UserUpdate{TOTPEnabled: &disabled}))

	stored, err := svc.db.GetUser(ctx, user.UID)
	r.NoError(err)

	secret := *stored.TOTPSecret
	setupToken, err := svc.generateTOTPSetupToken(user.UID, secret)
	r.NoError(err)

	code, err := totp.GenerateCode(secret, time.Now())
	r.NoError(err)

	_, err = svc.Confirm2FA(ctx, user.UID, Confirm2FARequest{Code: code, SetupToken: setupToken})
	r.NoError(err)

	_, err = svc.Verify2FA(ctx, mintTwoFATempToken(t, svc, org, user), code, Context{})
	r.ErrorIs(err, ErrInvalid2FACode, "the confirm code is spent")
}

func TestTOTPMatchedStepWithinSkew(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	secret := "JBSWY3DPEHPK3PXP"
	now := time.Unix(1_800_000_015, 0)
	step := int(now.Unix() / totpPeriod)

	for _, offset := range []int{-1, 0, 1} {
		code, err := totp.GenerateCode(secret, now.Add(time.Duration(offset)*totpPeriod*time.Second))
		r.NoError(err)

		got, ok := totpMatchedStep(code, secret, now)
		r.True(ok, "offset %d", offset)
		r.Equal(step+offset, got)
	}

	far, err := totp.GenerateCode(secret, now.Add(2*totpPeriod*time.Second))
	r.NoError(err)

	_, ok := totpMatchedStep(far, secret, now)
	r.False(ok, "a code two steps ahead is outside the skew")

	_, ok = totpMatchedStep("000000", "", now)
	r.False(ok, "no secret matches nothing")
}

func TestDisable2FASendsNotice(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	plain, org, user, secret := newTwoFAThrottleFixture(t, dbSvc, ctx, "rp-notice")
	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := NewService(dbSvc, plain.cfg, plain.fullCfg, jobs, nil)

	// The account's current address, changed after the fixture created it.
	newEmail := "2fa-rp-notice-new@acme.com"
	r.NoError(dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{Email: &newEmail}))

	code, err := totp.GenerateCode(secret, time.Now())
	r.NoError(err)
	r.NoError(svc.Disable2FA(signedInCtxFor(ctx, user.UID), user.UID, code))

	listed, err := dbSvc.ListJobs(ctx, nil, 0)
	r.NoError(err)

	var notices []*models.Job

	for _, job := range listed {
		if job.Type == string(jobdef.JobTypeEmail) && job.Config["template"] == email.TemplateTOTPDisabled {
			notices = append(notices, job)
		}
	}

	r.Len(notices, 1, "exactly one totp-disabled notice")
	r.Equal([]any{newEmail}, notices[0].Config["to"])

	data, ok := notices[0].Config["templateData"].(map[string]any)
	r.True(ok)
	r.NotEmpty(data["SecurityURL"])

	// The account keeps working: 2FA is off and the next sign-in needs no code.
	stored, err := dbSvc.GetUser(ctx, user.UID)
	r.NoError(err)
	r.False(stored.TOTPEnabled)
	r.Empty(*stored.TOTPSecret)
	r.Equal(newEmail, stored.Email)

	_, err = svc.completeLoginAfter2FA(ctx, stored, org.Slug, string(models.MemberRoleAdmin),
		AuthMethodLDAP, Context{})
	r.NoError(err)
}
