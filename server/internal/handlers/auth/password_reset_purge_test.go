package auth

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Spec 2026-11-02-05: a password or email change cuts every outstanding
// reset link of the user.
func TestPasswordResetLinksPurgedOnChange(t *testing.T) {
	t.Parallel()

	t.Run("using one reset link kills the others", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")
		user := passwordResetUser(t, ctx, dbSvc, "two-links@example.com")

		first, err := svc.storePasswordReset(ctx, user)
		r.NoError(err)
		second, err := svc.storePasswordReset(ctx, user)
		r.NoError(err)

		_, err = svc.ResetPassword(ctx, ResetPasswordRequest{Token: first, Password: "firstnewpassword"})
		r.NoError(err)

		_, err = svc.ResetPassword(ctx, ResetPasswordRequest{Token: second, Password: "secondnewpassword"})
		r.ErrorIs(err, ErrPasswordResetExpired)

		index, err := dbSvc.ListStateEntries(ctx, nil, passwordResetUserKeyPrefix+user.UID+":")
		r.NoError(err)
		r.Empty(index, "the per-user index is purged too")
	})

	t.Run("change password kills an earlier link, a later one works", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")
		user := passwordResetUser(t, ctx, dbSvc, "change-pw@example.com")

		before, err := svc.storePasswordReset(ctx, user)
		r.NoError(err)

		_, err = svc.ChangePassword(ctx, user.UID, "", ChangePasswordRequest{
			CurrentPassword: "oldpassword",
			NewPassword:     "changedpassword",
		})
		r.NoError(err)

		_, err = svc.ResetPassword(ctx, ResetPasswordRequest{Token: before, Password: "attackerpassword"})
		r.ErrorIs(err, ErrPasswordResetExpired)

		// Positive control: a link requested after the change still works.
		after, err := svc.storePasswordReset(ctx, user)
		r.NoError(err)

		_, err = svc.ResetPassword(ctx, ResetPasswordRequest{Token: after, Password: "legitpassword"})
		r.NoError(err)
	})

	t.Run("email change kills a link sent to the old address", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")
		user := passwordResetUser(t, ctx, dbSvc, "old-box@example.com")

		token, err := svc.storePasswordReset(ctx, user)
		r.NoError(err)

		r.NoError(svc.applyEmailChange(ctx, &emailChange{
			user:      user,
			newEmail:  "new-box@example.com",
			changedBy: emailChangedBySelf,
			actorUID:  user.UID,
		}))

		_, err = svc.ResetPassword(ctx, ResetPasswordRequest{Token: token, Password: "attackerpassword"})
		r.ErrorIs(err, ErrPasswordResetExpired)
	})

	t.Run("link refused once the address on file differs, even if not purged", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")
		user := passwordResetUser(t, ctx, dbSvc, "stale-box@example.com")

		token, err := svc.storePasswordReset(ctx, user)
		r.NoError(err)

		// Bypass the purge: write the new address straight to the row.
		newEmail := "fresh-box@example.com"
		r.NoError(dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{Email: &newEmail}))

		_, err = svc.ResetPassword(ctx, ResetPasswordRequest{Token: token, Password: "attackerpassword"})
		r.ErrorIs(err, ErrPasswordResetExpired)
	})

	t.Run("address match ignores case", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")
		user := passwordResetUser(t, ctx, dbSvc, "case-box@example.com")

		token, err := svc.storePasswordReset(ctx, user)
		r.NoError(err)

		upper := "Case-Box@Example.com"
		r.NoError(dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{Email: &upper}))

		_, err = svc.ResetPassword(ctx, ResetPasswordRequest{Token: token, Password: "legitpassword"})
		r.NoError(err)
	})
}
