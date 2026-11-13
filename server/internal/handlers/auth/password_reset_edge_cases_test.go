package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

const (
	resetEmail       = "alice@acme.com"
	resetOldPassword = "oldpassword"
	resetNewPassword = "brandnewpassword"
)

// softDeletedStateEntries selects the soft deletes of state entries.
//
//nolint:gochecknoglobals // test fixture
var softDeletedStateEntries = failingSQL(`UPDATE "state_entries"`, "SET deleted_at")

// resetWorld is a service with one password account and a mail queue, so a
// test gets the reset link the way the user does: from the email.
type resetWorld struct {
	svc   *Service
	db    db.Service
	ctx   context.Context //nolint:containedctx // test fixture
	fault *queryFault
	user  *models.User
}

func newResetWorld(t *testing.T) *resetWorld {
	t.Helper()

	cfg := &config.Config{
		Server: config.ServerConfig{BaseURL: "http://127.0.0.1:4000"},
		Auth: config.AuthConfig{
			JWTSecret:          "test-jwt-secret",
			AccessTokenExpiry:  time.Hour,
			RefreshTokenExpiry: 7 * 24 * time.Hour,
		},
	}
	svc, dbSvc, ctx := setupAuthTestServiceWithJobs(t, cfg)

	user := models.NewUser(resetEmail)
	hash := "$plaintext$" + resetOldPassword
	user.PasswordHash = &hash
	require.NoError(t, dbSvc.CreateUser(ctx, user))

	return &resetWorld{svc: svc, db: dbSvc, ctx: ctx, fault: installQueryFault(dbSvc), user: user}
}

// requestLink asks for a reset and returns the token of the mailed link.
func (w *resetWorld) requestLink(t *testing.T) string {
	t.Helper()

	_, err := w.svc.RequestPasswordReset(w.ctx, RequestPasswordResetRequest{Email: resetEmail}, "")
	require.NoError(t, err)

	_, data := latestEmailJobTemplateData(t, w.ctx, w.db)
	link, _ := data["ResetURL"].(string)
	require.NotEmpty(t, link)

	return link[strings.LastIndex(link, "/")+1:]
}

func (w *resetWorld) reset(token string) error {
	_, err := w.svc.ResetPassword(w.ctx, ResetPasswordRequest{Token: token, Password: resetNewPassword})

	return err
}

// passwordIs reports whether the account signs in with password.
func (w *resetWorld) passwordIs(t *testing.T, password string) bool {
	t.Helper()

	user, err := w.db.GetUser(w.ctx, w.user.UID)
	require.NoError(t, err)

	return w.svc.verifyLocalPassword(w.ctx, user, password) == nil
}

func (w *resetWorld) pendingLinks(t *testing.T) int {
	t.Helper()

	entries, err := w.db.ListStateEntries(w.ctx, nil, passwordResetKeyPrefix)
	require.NoError(t, err)

	return len(entries)
}

// TestRequestPasswordResetWhenTheStoreFails: a link that could not be stored
// is an error (the mail would carry a dead link), and no mail leaves.
func TestRequestPasswordResetWhenTheStoreFails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fault   sqlFault
		wantMsg string
	}{
		{"reset entry", failingSQL(`"state_entries"`, `'{"emailHash"`), "failed to store password reset"},
		{"per-user index entry", failingSQL(`"state_entries"`, `SET value = '{"userUid"`), "failed to index password reset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := newResetWorld(t)

			var err error

			w.fault.during(t, tt.fault, func() {
				_, err = w.svc.RequestPasswordReset(w.ctx, RequestPasswordResetRequest{Email: resetEmail}, "")
			})
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorContains(t, err, tt.wantMsg)

			jobs, err := w.db.ListJobs(w.ctx, nil, 0)
			require.NoError(t, err)
			require.Empty(t, jobs, "no reset email for a link that was not stored")
		})
	}

	t.Run("linked providers of a password-less account cannot be listed", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		w := newResetWorld(t)
		noPassword := ""
		r.NoError(w.db.UpdateUser(w.ctx, w.user.UID, &models.UserUpdate{PasswordHash: &noPassword}))
		r.NoError(w.db.CreateUserProvider(w.ctx,
			models.NewUserProvider(w.user.UID, models.ProviderTypeGoogle, "google-1")))

		var (
			resp *RequestPasswordResetResponse
			err  error
		)

		w.fault.during(t, failingSQL(`FROM "user_providers"`), func() {
			resp, err = w.svc.RequestPasswordReset(w.ctx, RequestPasswordResetRequest{Email: resetEmail}, "")
		})
		r.NoError(err, "the answer never tells whether the account exists")
		r.NotEmpty(resp.Message)
		r.Zero(w.pendingLinks(t))

		// Control: with a healthy store the same account gets its link.
		r.NotEmpty(w.requestLink(t))
	})
}

// TestResetPasswordWithAnUnusableEntry: a reset entry that names no user, or
// a user that is gone, is an expired link.
func TestResetPasswordWithAnUnusableEntry(t *testing.T) {
	t.Parallel()

	t.Run("entry without a user", func(t *testing.T) {
		t.Parallel()

		w := newResetWorld(t)
		require.NoError(t, w.db.SetStateEntry(w.ctx, nil,
			passwordResetKeyPrefix+hashPendingToken("orphan-token"), &models.JSONMap{"note": "no user"}, nil))

		require.ErrorIs(t, w.reset("orphan-token"), ErrPasswordResetExpired)
	})

	t.Run("user deleted after the request", func(t *testing.T) {
		t.Parallel()

		w := newResetWorld(t)
		token := w.requestLink(t)
		require.NoError(t, w.db.DeleteUser(w.ctx, w.user.UID))

		require.ErrorIs(t, w.reset(token), ErrPasswordResetExpired)
	})

	t.Run("entry cannot be read", func(t *testing.T) {
		t.Parallel()

		w := newResetWorld(t)
		token := w.requestLink(t)

		var err error

		w.fault.during(t, failingSQL(`FROM "state_entries"`), func() { err = w.reset(token) })
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorContains(t, err, "failed to load password reset entry")
		require.True(t, w.passwordIs(t, resetOldPassword))
	})
}

// TestResetPasswordWhenTheStoreFails: only the password write decides the
// outcome. The cleanup after it is best effort.
func TestResetPasswordWhenTheStoreFails(t *testing.T) {
	t.Parallel()

	t.Run("password cannot be written: nothing changes and the link stays usable", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		w := newResetWorld(t)
		token := w.requestLink(t)

		var err error

		w.fault.during(t, failingSQL(`UPDATE "users"`), func() { err = w.reset(token) })
		r.ErrorIs(err, context.Canceled)
		r.ErrorContains(err, "failed to update password")
		r.True(w.passwordIs(t, resetOldPassword))

		r.NoError(w.reset(token))
		r.True(w.passwordIs(t, resetNewPassword))
	})

	t.Run("entries cannot be deleted: the password is changed anyway", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		w := newResetWorld(t)
		token := w.requestLink(t)
		links := w.pendingLinks(t)

		var err error

		w.fault.during(t, softDeletedStateEntries, func() { err = w.reset(token) })
		r.NoError(err)
		r.True(w.passwordIs(t, resetNewPassword))
		r.Equal(links, w.pendingLinks(t), "the entries age out at their TTL instead")
	})

	t.Run("sessions cannot be revoked: the password is changed anyway", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		w := newResetWorld(t)
		token := w.requestLink(t)
		session := models.NewUserToken(w.user.UID, nil, "session-value", models.TokenTypeRefresh)
		r.NoError(w.db.CreateUserToken(w.ctx, session))

		var err error

		w.fault.during(t, failingSQL(`UPDATE "user_tokens"`), func() { err = w.reset(token) })
		r.NoError(err)
		r.True(w.passwordIs(t, resetNewPassword))

		_, err = w.db.GetUserToken(w.ctx, session.UID)
		r.NoError(err, "the session row could not be deleted")
	})
}

// TestResetPasswordPurgesOnlyTheUsersOwnLinks: the purge lists entries with a
// LIKE, where "_" matches any character. A key that only looks like the
// user's index prefix is left alone.
func TestResetPasswordPurgesOnlyTheUsersOwnLinks(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newResetWorld(t)
	first := w.requestLink(t)
	second := w.requestLink(t)

	lookalike := strings.ReplaceAll(passwordResetUserKeyPrefix, "_", "-") + w.user.UID + ":abc"
	r.NoError(w.db.SetStateEntry(w.ctx, nil, lookalike, &models.JSONMap{"keep": true}, nil))

	r.NoError(w.reset(first))

	r.ErrorIs(w.reset(second), ErrPasswordResetExpired, "every other link of the user died with the first")
	r.Zero(w.pendingLinks(t))

	entry, err := w.db.GetStateEntry(w.ctx, nil, lookalike)
	r.NoError(err)
	r.NotNil(entry, "the lookalike key is not one of the user's links")
}

// TestNoMailIsQueuedForAnAddressThatIsNotOne: an account whose stored address
// cannot receive mail gets the usual answer, and no email job.
func TestNoMailIsQueuedForAnAddressThatIsNotOne(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	w := newResetWorld(t)

	const handle = "not-an-address"

	user := models.NewUser(handle)
	hash := "$plaintext$" + resetOldPassword
	user.PasswordHash = &hash
	r.NoError(w.db.CreateUser(w.ctx, user))

	resp, err := w.svc.RequestPasswordReset(w.ctx, RequestPasswordResetRequest{Email: handle}, "")
	r.NoError(err)
	r.NotEmpty(resp.Message)

	jobs, err := w.db.ListJobs(w.ctx, nil, 0)
	r.NoError(err)
	r.Empty(jobs)

	// Control: the queue works for a deliverable address.
	r.NotEmpty(w.requestLink(t))
}
