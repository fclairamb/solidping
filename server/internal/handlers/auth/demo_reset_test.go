package auth

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// TestResetPasswordRefusesTheDemoAccount covers one of exactly two paths the
// demo write guard cannot see, because it is unauthenticated (spec
// 2026-09-06-02).
//
// The demo password is PUBLISHED. Anyone holding the demo mailbox — or any
// visitor who clicked "forgot password" out of curiosity — could otherwise
// rotate it and lock every other visitor out of the live demo, silently and
// permanently, with the guard none the wiser.
func TestResetPasswordRefusesTheDemoAccount(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")

	user := passwordResetUser(t, ctx, dbSvc, "demo@solidping.example")
	demo := true
	r.NoError(dbSvc.UpdateUser(ctx, user.UID, &models.UserUpdate{Demo: &demo}))

	token := "token-for-the-demo-account"
	stateValue := &models.JSONMap{"userUid": user.UID}
	ttl := passwordResetTTL
	r.NoError(dbSvc.SetStateEntry(ctx, nil,
		passwordResetKeyPrefix+hashResetToken(token), stateValue, &ttl))

	_, err := svc.ResetPassword(ctx, ResetPasswordRequest{Token: token, Password: "newpassword"})
	r.ErrorIs(err, ErrDemoAccountNotResettable)

	// The refusal must be real, not cosmetic: the stored hash still verifies
	// against the ORIGINAL password.
	after, err := dbSvc.GetUser(ctx, user.UID)
	r.NoError(err)
	r.NotNil(after.PasswordHash)
	r.True(passwords.Verify("oldpassword", *after.PasswordHash),
		"the demo password was rotated despite the refusal")
	r.False(passwords.Verify("newpassword", *after.PasswordHash))
}

// TestResetPasswordStillWorksForOrdinaryAccounts is the positive control for
// the test above: without it, a refusal that accidentally rejected EVERY reset
// would look like a pass.
func TestResetPasswordStillWorksForOrdinaryAccounts(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, ctx := setupAuthTestServiceWithConfig(t, "https://example.com")

	user := passwordResetUser(t, ctx, dbSvc, "ordinary@solidping.example")

	token := "token-for-an-ordinary-account"
	stateValue := &models.JSONMap{"userUid": user.UID}
	ttl := passwordResetTTL
	r.NoError(dbSvc.SetStateEntry(ctx, nil,
		passwordResetKeyPrefix+hashResetToken(token), stateValue, &ttl))

	_, err := svc.ResetPassword(ctx, ResetPasswordRequest{Token: token, Password: "newpassword"})
	r.NoError(err)

	after, err := dbSvc.GetUser(ctx, user.UID)
	r.NoError(err)
	r.True(passwords.Verify("newpassword", *after.PasswordHash))
}

// TestNewUserInfoCarriesTheDemoFlag pins the /auth/me and login-response shape
// the dashboard routes on: without it the banner never appears and the demo
// looks like an ordinary account until the first write is refused.
func TestNewUserInfoCarriesTheDemoFlag(t *testing.T) {
	t.Parallel()

	demoUser := models.NewUser("demo@solidping.example")
	demoUser.Demo = true
	require.True(t, newUserInfo(demoUser, "user").Demo)

	plain := models.NewUser("plain@solidping.example")
	require.False(t, newUserInfo(plain, "user").Demo,
		"an ordinary account must not be advertised as the demo")
}
