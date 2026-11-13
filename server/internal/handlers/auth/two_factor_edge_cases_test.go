package auth

import (
	"context"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/sealing"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

// twoFAWorld is a member with TOTP enabled who just passed the password step:
// temp is the challenge token the second step redeems.
type twoFAWorld struct {
	*edgeWorld

	slug   string
	user   *models.User
	secret string
	temp   string
}

func newTwoFAWorld(t *testing.T) *twoFAWorld {
	t.Helper()

	w := newEdgeWorld(t)
	slug, user, _ := w.signedIn(t, models.MemberRoleOwner)
	secret := enableTestTOTP(w.ctx, t, w.dbSvc, user)

	// The challenge token the password step hands out for this account.
	temp, err := w.svc.generate2FATempToken(user.UID, slug, string(models.MemberRoleOwner), AuthMethodPassword)
	require.NoError(t, err)

	return &twoFAWorld{edgeWorld: w, slug: slug, user: user, secret: secret, temp: temp}
}

func (w *twoFAWorld) code(t *testing.T) string {
	t.Helper()

	code, err := totp.GenerateCode(w.secret, time.Now())
	require.NoError(t, err)

	return code
}

// secondStep is one way to answer the challenge.
type secondStep struct {
	name string
	run  func(t *testing.T, w *twoFAWorld) (*LoginResponse, error)
}

func secondSteps() []secondStep {
	return []secondStep{
		{"TOTP code", func(t *testing.T, w *twoFAWorld) (*LoginResponse, error) {
			t.Helper()

			return w.svc.Verify2FA(w.ctx, w.temp, w.code(t), edgeAuthContext)
		}},
		{"recovery code", func(t *testing.T, w *twoFAWorld) (*LoginResponse, error) {
			t.Helper()

			return w.svc.Recovery2FA(w.ctx, w.temp, "recovery-one", edgeAuthContext)
		}},
	}
}

// TestSecondFactorStepRefusals: what changed between the password step and
// the second step is checked again, and a store failure is an error. In
// every case no session is minted.
func TestSecondFactorStepRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spoil   func(t *testing.T, w *twoFAWorld)
		fault   *sqlFault
		want    error
		wantMsg string
	}{
		{
			name: "user deleted since the challenge", want: ErrUserNotFound,
			spoil: func(t *testing.T, w *twoFAWorld) {
				t.Helper()
				require.NoError(t, w.dbSvc.DeleteUser(w.ctx, w.user.UID))
			},
		},
		{
			name: "2FA turned off since the challenge", want: ErrTwoFANotEnabled,
			spoil: func(t *testing.T, w *twoFAWorld) {
				t.Helper()

				off := false
				require.NoError(t, w.dbSvc.UpdateUser(w.ctx, w.user.UID, &models.UserUpdate{TOTPEnabled: &off}))
			},
		},
		{
			name: "organization deleted since the challenge", want: ErrOrganizationNotFound,
			spoil: func(t *testing.T, w *twoFAWorld) {
				t.Helper()
				require.NoError(t, w.dbSvc.DeleteOrganization(w.ctx, w.org(t, w.slug).UID))
			},
		},
		{
			name: "attempt counter cannot be written", want: context.Canceled,
			fault: &sqlFault{fragments: []string{`"state_entries"`}}, wantMsg: "failed to bump 2FA attempt counter",
		},
		{
			name: "user cannot be read", want: context.Canceled,
			fault: &sqlFault{fragments: []string{`FROM "users"`}},
		},
		{
			name: "organization cannot be read", want: context.Canceled,
			fault: &sqlFault{fragments: []string{`FROM "organizations"`}},
		},
		{
			name: "session cannot be stored", want: context.Canceled,
			fault: &sqlFault{fragments: []string{`INSERT INTO "user_tokens"`}},
		},
	}
	for _, step := range secondSteps() {
		for _, tt := range tests {
			t.Run(step.name+"/"+tt.name, func(t *testing.T) {
				t.Parallel()

				w := newTwoFAWorld(t)
				sessions := w.sessionCount(t, w.user.UID)

				if tt.spoil != nil {
					tt.spoil(t, w)
				}

				var (
					resp *LoginResponse
					err  error
				)

				if tt.fault != nil {
					w.fault.during(t, *tt.fault, func() { resp, err = step.run(t, w) })
				} else {
					resp, err = step.run(t, w)
				}

				require.ErrorIs(t, err, tt.want)
				require.ErrorContains(t, err, tt.wantMsg)
				require.Nil(t, resp)
				require.Equal(t, sessions, w.sessionCount(t, w.user.UID))
			})
		}
	}
}

// TestSecondFactorStepBestEffortWrites: the activity stamp and the
// organization list are decorative, their failure does not fail the login.
func TestSecondFactorStepBestEffortWrites(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		fault    sqlFault
		wantOrgs int
	}{
		{"last activity stamp", failingSQL(`UPDATE "users"`, "last_active_at"), 1},
		{"organization list", failingSQL(`FROM "organization_members"`), 0},
		{"attempt counter reset", softDeletedStateEntries, 1},
	}
	for _, step := range secondSteps() {
		for _, tt := range tests {
			t.Run(step.name+"/"+tt.name, func(t *testing.T) {
				t.Parallel()

				w := newTwoFAWorld(t)

				var (
					resp *LoginResponse
					err  error
				)

				w.fault.during(t, tt.fault, func() { resp, err = step.run(t, w) })
				require.NoError(t, err)
				require.NotEmpty(t, resp.RefreshToken)
				require.Equal(t, w.slug, resp.Organization.Slug)
				require.Len(t, resp.Organizations, tt.wantOrgs)
			})
		}
	}
}

func TestRecoveryCodeIsNotSpentWhenItCannotBeRemoved(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newTwoFAWorld(t)

	var err error

	w.fault.during(t, failingSQL(`UPDATE "users"`, "totp_recovery_codes"), func() {
		_, err = w.svc.Recovery2FA(w.ctx, w.temp, "recovery-one", edgeAuthContext)
	})
	r.ErrorIs(err, context.Canceled)

	resp, err := w.svc.Recovery2FA(w.ctx, w.temp, "recovery-one", edgeAuthContext)
	r.NoError(err, "the code was not consumed by the failed attempt")
	r.NotEmpty(resp.RefreshToken)

	_, err = w.svc.Recovery2FA(w.ctx, w.temp, "recovery-one", edgeAuthContext)
	r.ErrorIs(err, ErrInvalidRecoveryCode, "and it is single use")
}

func TestDisable2FAKeepsTheFactorWhenItCannotBeWritten(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newTwoFAWorld(t)

	var err error

	w.fault.during(t, failingSQL(`UPDATE "users"`), func() {
		err = w.svc.Disable2FA(w.ctx, w.user.UID, w.code(t))
	})
	r.ErrorIs(err, context.Canceled)

	stored, err := w.dbSvc.GetUser(w.ctx, w.user.UID)
	r.NoError(err)
	r.True(stored.TOTPEnabled)
}

func TestConfirm2FAWhenAlreadyEnabled(t *testing.T) {
	t.Parallel()

	w := newTwoFAWorld(t)

	_, err := w.svc.Confirm2FA(w.ctx, w.user.UID, Confirm2FARequest{Code: w.code(t)})
	require.ErrorIs(t, err, ErrTwoFAAlreadyEnabled)
}

// TestUpdateOrgSettingsSurfacesStoreFailures: a setting that could not be
// stored fails the update, and reads back unchanged.
func TestUpdateOrgSettingsSurfacesStoreFailures(t *testing.T) { //nolint:tparallel // subtests share one fixture
	t.Parallel()

	w := newEdgeWorld(t)
	slug, _, _ := w.signedIn(t, models.MemberRoleOwner)

	hour := 3600
	reminder := 900
	origins := []string{"https://status.acme.com"}
	required := sealing.ModeRequired

	before, err := w.svc.GetOrgSettings(w.ctx, slug)
	require.NoError(t, err)

	tests := []struct {
		name    string
		req     UpdateOrgSettingsRequest
		key     string
		wantMsg string
	}{
		{
			"session max duration",
			UpdateOrgSettingsRequest{SessionMaxDurationSeconds: &hour},
			string(systemconfig.KeySessionMaxDuration), "",
		},
		{
			"status page embed origins",
			UpdateOrgSettingsRequest{StatusPageAllowedEmbedOrigins: &origins},
			models.ParamKeyStatusPageAllowedEmbedOrigins, "",
		},
		{
			"reminder interval",
			UpdateOrgSettingsRequest{ReminderIntervalSeconds: &reminder},
			models.ParamKeyIncidentReminderInterval, "",
		},
		{
			"browser sealing mode",
			UpdateOrgSettingsRequest{BrowserSealingMode: &required},
			sealing.ParamKeyBrowserSealingMode, "set browser sealing mode",
		},
	}
	for _, tt := range tests { //nolint:paralleltest // subtests share one fixture and run in order
		t.Run(tt.name, func(t *testing.T) {
			var updateErr error

			w.fault.during(t, failingSQL(`"parameters"`, tt.key), func() {
				_, updateErr = w.svc.UpdateOrgSettings(w.ctx, slug, tt.req)
			})
			require.ErrorIs(t, updateErr, context.Canceled)
			require.ErrorContains(t, updateErr, tt.wantMsg)

			after, err := w.svc.GetOrgSettings(w.ctx, slug)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}

	// Control: stored when the store is healthy.
	{
		after, err := w.svc.UpdateOrgSettings(w.ctx, slug, UpdateOrgSettingsRequest{
			SessionMaxDurationSeconds: &hour, StatusPageAllowedEmbedOrigins: &origins,
			ReminderIntervalSeconds: &reminder, BrowserSealingMode: &required,
		})
		require.NoError(t, err)
		require.Equal(t, &hour, after.SessionMaxDurationSeconds)
		require.Equal(t, origins, after.StatusPageAllowedEmbedOrigins)
		require.NotEqual(t, before, after)
	}
}
