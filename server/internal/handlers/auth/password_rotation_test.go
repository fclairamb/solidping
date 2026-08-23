package auth

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// TestPasswordRotationRequired pins the predicate every surface consults.
//
// The negative case that matters most is the SSO-only account: it has no
// password at all, so if the column's mere existence — or a nil-user path —
// ever flipped this to true, those users would be locked out of a rotation
// they cannot perform.
func TestPasswordRotationRequired(t *testing.T) {
	t.Parallel()

	hash := "irrelevant"

	cases := []struct {
		name string
		user *models.User
		want bool
	}{
		{"nil user", nil, false},
		{"ordinary user", &models.User{PasswordHash: &hash}, false},
		{"sso user with no password", &models.User{PasswordHash: nil}, false},
		{"flagged user", &models.User{PasswordHash: &hash, MustChangePassword: true}, true},
		{"flagged sso user", &models.User{PasswordHash: nil, MustChangePassword: true}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, PasswordRotationRequired(tc.user))
		})
	}
}

// TestIsPasswordRotationExempt pins the allowlist exactly. The denied entries
// are the point of the test: a near-miss on method or path must NOT slip
// through, because the allowlist is the whole width of the hole a flagged
// session can reach through.
func TestIsPasswordRotationExempt(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{"rotation endpoint", http.MethodPost, "/api/v1/auth/change-password", true},
		{"me", http.MethodGet, "/api/v1/auth/me", true},
		{"me with trailing slash", http.MethodGet, "/api/v1/auth/me/", true},
		{"logout", http.MethodPost, "/api/v1/auth/logout", true},
		{"cors preflight", http.MethodOptions, "/api/v1/orgs/acme/checks", true},

		{"me with wrong method", http.MethodPatch, "/api/v1/auth/me", false},
		{"rotation with wrong method", http.MethodGet, "/api/v1/auth/change-password", false},
		{"prefix lookalike", http.MethodGet, "/api/v1/auth/me/tokens", false},
		{"suffix lookalike", http.MethodPost, "/api/v1/auth/change-password-now", false},
		{"pat creation", http.MethodPost, "/api/v1/orgs/acme/tokens", false},
		{"checks", http.MethodGet, "/api/v1/orgs/acme/checks", false},
		{"mcp", http.MethodPost, "/api/v1/mcp", false},
		{"root", http.MethodGet, "/", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, IsPasswordRotationExempt(tc.method, tc.path))
		})
	}
}

// TestChangePasswordClearsRotationFlag is the "clearing the flag restores
// normal access" half of the feature, at the service layer: the one action a
// flagged session can take must actually un-flag the account.
func TestChangePasswordClearsRotationFlag(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	user := passwordResetUser(t, ctx, dbSvc, "forced-rotation@example.com")

	flagged := true
	r.NoError(dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{MustChangePassword: &flagged}))

	before, err := dbSvc.GetUser(ctx, user.UID)
	r.NoError(err)
	r.True(before.MustChangePassword, "precondition: the account is flagged")

	_, err = svc.ChangePassword(ctx, user.UID, "", ChangePasswordRequest{
		CurrentPassword: "oldpassword",
		NewPassword:     "brandnewpassword",
	})
	r.NoError(err)

	after, err := dbSvc.GetUser(ctx, user.UID)
	r.NoError(err)
	r.False(after.MustChangePassword, "a completed rotation must clear the flag")
	r.True(passwords.Verify("brandnewpassword", *after.PasswordHash))
}

// TestChangePasswordRejectionKeepsRotationFlag is the positive control for the
// test above: a rotation that FAILS must leave the block in place. Without
// this, "the flag is cleared" would also pass if ChangePassword cleared it
// unconditionally, which would turn a wrong-password attempt into an escape.
func TestChangePasswordRejectionKeepsRotationFlag(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestService(t)
	user := passwordResetUser(t, ctx, dbSvc, "forced-rotation-fail@example.com")

	flagged := true
	r.NoError(dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{MustChangePassword: &flagged}))

	_, err := svc.ChangePassword(ctx, user.UID, "", ChangePasswordRequest{
		CurrentPassword: "wrong-password",
		NewPassword:     "brandnewpassword",
	})
	r.ErrorIs(err, ErrInvalidCurrentPassword)

	after, err := dbSvc.GetUser(ctx, user.UID)
	r.NoError(err)
	r.True(after.MustChangePassword, "a rejected rotation must not lift the block")
}

// TestUpdateUserLeavesRotationFlagAloneWhenNil pins the nil-means-untouched
// contract on UserUpdate: an unrelated profile write must never silently
// un-force a pending rotation.
func TestUpdateUserLeavesRotationFlagAloneWhenNil(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, dbSvc, ctx := setupAuthTestService(t)
	user := passwordResetUser(t, ctx, dbSvc, "untouched-flag@example.com")

	flagged := true
	r.NoError(dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{MustChangePassword: &flagged}))

	newName := "Renamed"
	r.NoError(dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{Name: &newName}))

	after, err := dbSvc.GetUser(ctx, user.UID)
	r.NoError(err)
	r.Equal("Renamed", after.Name)
	r.True(after.MustChangePassword, "an unrelated update must not clear the rotation flag")
}

// TestNewUserInfoCarriesRotationSignal pins the machine-readable signal on the
// payload every login-shaped response and /auth/me share. Without it a client
// can only discover the state by tripping a 403 it would render as a generic
// permission error.
func TestNewUserInfoCarriesRotationSignal(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	user := models.NewUser("signal@example.com")
	r.False(newUserInfo(user, "owner").MustChangePassword)

	user.MustChangePassword = true
	info := newUserInfo(user, "owner")
	r.True(info.MustChangePassword)
	r.Equal("owner", info.Role)

	r.Nil(newUserInfo(nil, "owner"))
}
