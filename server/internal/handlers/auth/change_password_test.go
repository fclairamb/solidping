package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// ssoOnlyUser seeds a user with no password hash at all — the account shape
// produced by an OAuth/SSO-only sign-up.
//
//nolint:revive // ctx-second matches the existing helpers in this package
func ssoOnlyUser(t *testing.T, ctx context.Context, dbSvc db.Service, email string) *models.User {
	t.Helper()

	user := models.NewUser(email)
	user.PasswordHash = nil
	require.NoError(t, dbSvc.CreateUser(ctx, user))

	return user
}

// seedRefreshToken creates a session refresh-token row for the user and
// returns it, so tests can assert which rows survive a rotation.
//
//nolint:revive // ctx-second matches the existing helpers in this package
func seedRefreshToken(t *testing.T, ctx context.Context, dbSvc db.Service, userUID, label string) *models.UserToken {
	t.Helper()

	token := &models.UserToken{
		UID:     uuidV7(t),
		UserUID: userUID,
		Type:    models.TokenTypeRefresh,
		Token:   label + "-" + userUID,
	}
	require.NoError(t, dbSvc.CreateUserToken(ctx, token))

	return token
}

func TestChangePasswordValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		currentPassword string
		newPassword     string
		wantErr         error
	}{
		{
			name:            "wrong current password",
			currentPassword: "notmypassword",
			newPassword:     "brandnewpassword",
			wantErr:         ErrInvalidCurrentPassword,
		},
		{
			name:            "missing current password when one is set",
			currentPassword: "",
			newPassword:     "brandnewpassword",
			wantErr:         ErrInvalidCurrentPassword,
		},
		{
			name:            "new password too short",
			currentPassword: "oldpassword",
			newPassword:     "short",
			wantErr:         ErrInvalidCredentials,
		},
		{
			name:            "new password identical to the current one",
			currentPassword: "oldpassword",
			newPassword:     "oldpassword",
			wantErr:         ErrInvalidCredentials,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			svc, dbSvc, ctx := setupAuthTestService(t)
			user := passwordResetUser(t, ctx, dbSvc, tc.name+"@example.com")
			originalHash := *user.PasswordHash

			_, err := svc.ChangePassword(ctx, user.UID, "", ChangePasswordRequest{
				CurrentPassword: tc.currentPassword,
				NewPassword:     tc.newPassword,
			})
			r.ErrorIs(err, tc.wantErr)

			// Nothing was persisted on any rejection path.
			refreshed, err := dbSvc.GetUser(ctx, user.UID)
			r.NoError(err)
			r.Equal(originalHash, *refreshed.PasswordHash)
		})
	}
}

func TestChangePasswordHappyPath(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	user := passwordResetUser(t, ctx, dbSvc, "happy-change@example.com")

	pat := &models.UserToken{
		UID:     uuidV7(t),
		UserUID: user.UID,
		Type:    models.TokenTypePAT,
		Token:   "pat-" + user.UID,
	}
	r.NoError(dbSvc.CreateUserToken(ctx, pat))

	resp, err := svc.ChangePassword(ctx, user.UID, "", ChangePasswordRequest{
		CurrentPassword: "oldpassword",
		NewPassword:     "brandnewpassword",
	})
	r.NoError(err)
	r.NotEmpty(resp.Message)

	updated, err := dbSvc.GetUser(ctx, user.UID)
	r.NoError(err)
	r.NotNil(updated.PasswordHash)
	r.False(passwords.Verify("oldpassword", *updated.PasswordHash))
	r.True(passwords.Verify("brandnewpassword", *updated.PasswordHash))

	// PATs are separately managed credentials and must survive.
	pats, err := dbSvc.ListUserTokensByType(ctx, user.UID, models.TokenTypePAT)
	r.NoError(err)
	r.Len(pats, 1)
}

// TestChangePasswordSetsInitialPasswordForSSOUser pins the SSO escape hatch:
// an account with no password hash can set one, and currentPassword is
// irrelevant on that path (here: deliberately garbage).
func TestChangePasswordSetsInitialPasswordForSSOUser(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	user := ssoOnlyUser(t, ctx, dbSvc, "sso-change@example.com")

	_, err := svc.ChangePassword(ctx, user.UID, "", ChangePasswordRequest{
		CurrentPassword: "irrelevant-garbage",
		NewPassword:     "myfirstpassword",
	})
	r.NoError(err)

	updated, err := dbSvc.GetUser(ctx, user.UID)
	r.NoError(err)
	r.NotNil(updated.PasswordHash)
	r.True(passwords.Verify("myfirstpassword", *updated.PasswordHash))
}

// TestChangePasswordSSOStillEnforcesMinLength is the positive control for the
// SSO path above: skipping the current-password check must not also skip the
// length rule.
func TestChangePasswordSSOStillEnforcesMinLength(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	user := ssoOnlyUser(t, ctx, dbSvc, "sso-short@example.com")

	_, err := svc.ChangePassword(ctx, user.UID, "", ChangePasswordRequest{NewPassword: "short"})
	r.ErrorIs(err, ErrInvalidCredentials)

	refreshed, err := dbSvc.GetUser(ctx, user.UID)
	r.NoError(err)
	r.Nil(refreshed.PasswordHash)
}

// TestChangePasswordSparesCallerSession is the core session-survival
// guarantee: the caller's own refresh-token row (Claims.RefreshUID) is spared
// while every other session of the same user is revoked.
func TestChangePasswordSparesCallerSession(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	user := passwordResetUser(t, ctx, dbSvc, "sessions@example.com")

	caller := seedRefreshToken(t, ctx, dbSvc, user.UID, "caller")
	other := seedRefreshToken(t, ctx, dbSvc, user.UID, "other")

	_, err := svc.ChangePassword(ctx, user.UID, caller.UID, ChangePasswordRequest{
		CurrentPassword: "oldpassword",
		NewPassword:     "brandnewpassword",
	})
	r.NoError(err)

	remaining, err := dbSvc.ListUserTokensByType(ctx, user.UID, models.TokenTypeRefresh)
	r.NoError(err)
	r.Len(remaining, 1)
	r.Equal(caller.UID, remaining[0].UID)

	// The caller's grant still resolves by its token value...
	live, err := dbSvc.GetUserTokenByToken(ctx, caller.Token)
	r.NoError(err)
	r.NotNil(live)
	r.Equal(caller.UID, live.UID)

	// ...and the second session's does not.
	dead, err := dbSvc.GetUserTokenByToken(ctx, other.Token)
	if err == nil {
		r.Nil(dead)
	}
}

// TestChangePasswordWithoutCallerRefreshUIDRevokesEverything pins the negative
// control for the sparing logic: an empty currentRefreshUID must not
// accidentally spare a row (e.g. by matching an empty UID).
func TestChangePasswordWithoutCallerRefreshUIDRevokesEverything(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	user := passwordResetUser(t, ctx, dbSvc, "no-refresh-uid@example.com")

	seedRefreshToken(t, ctx, dbSvc, user.UID, "a")
	seedRefreshToken(t, ctx, dbSvc, user.UID, "b")

	_, err := svc.ChangePassword(ctx, user.UID, "", ChangePasswordRequest{
		CurrentPassword: "oldpassword",
		NewPassword:     "brandnewpassword",
	})
	r.NoError(err)

	remaining, err := dbSvc.ListUserTokensByType(ctx, user.UID, models.TokenTypeRefresh)
	r.NoError(err)
	r.Empty(remaining)
}

// TestChangePasswordRateLimit proves the endpoint cannot be turned into an
// unbounded current-password oracle by a stolen session token.
func TestChangePasswordRateLimit(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	user := passwordResetUser(t, ctx, dbSvc, "ratelimit-change@example.com")

	for i := 0; i < changePasswordMaxPerUser; i++ {
		_, err := svc.ChangePassword(ctx, user.UID, "", ChangePasswordRequest{
			CurrentPassword: "guess",
			NewPassword:     "brandnewpassword",
		})
		r.ErrorIs(err, ErrInvalidCurrentPassword)
	}

	_, err := svc.ChangePassword(ctx, user.UID, "", ChangePasswordRequest{
		CurrentPassword: "guess",
		NewPassword:     "brandnewpassword",
	})
	r.ErrorIs(err, ErrRateLimited)

	// Even the *correct* password is refused once the cap is reached — the
	// limiter guards attempts, not outcomes.
	_, err = svc.ChangePassword(ctx, user.UID, "", ChangePasswordRequest{
		CurrentPassword: "oldpassword",
		NewPassword:     "brandnewpassword",
	})
	r.ErrorIs(err, ErrRateLimited)
}

// TestChangePasswordSuccessClearsRateLimitCounter keeps a legitimate user from
// being locked out by their own successful rotations.
func TestChangePasswordSuccessClearsRateLimitCounter(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	user := passwordResetUser(t, ctx, dbSvc, "counter-clear@example.com")

	_, err := svc.ChangePassword(ctx, user.UID, "", ChangePasswordRequest{
		CurrentPassword: "oldpassword",
		NewPassword:     "brandnewpassword",
	})
	r.NoError(err)

	entry, err := dbSvc.GetStateEntry(ctx, nil, changePasswordCountKeyPrefix+user.UID)
	r.NoError(err)
	r.Nil(entry)
}

func TestChangePasswordUnknownUser(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, ctx := setupAuthTestService(t)

	_, err := svc.ChangePassword(ctx, uuidV7(t), "", ChangePasswordRequest{
		CurrentPassword: "whatever",
		NewPassword:     "brandnewpassword",
	})
	r.ErrorIs(err, ErrUserNotFound)
}
