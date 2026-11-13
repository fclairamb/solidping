package telegramcb

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// Spec 2026-11-13-03: with Sign in with Telegram on, a linked chat signs in to
// the account, so a link that makes it a sign-in method emails the owner.

// withLinkNotice rebuilds the env's handler with the real auth service as the
// link notifier, over a job queue the notices can be read back from.
func (e *tgEnv) withLinkNotice(t *testing.T, loginEnabled bool) {
	t.Helper()

	cfg := &config.Config{}
	cfg.Server.BaseURL = "https://solidping.example"
	cfg.Telegram = config.TelegramConfig{
		Enabled:       config.BoolPtr(true),
		BotToken:      "123:AAtest",
		BotUsername:   "solidping_test_bot",
		WebhookSecret: testWebhookSecret,
		LoginEnabled:  loginEnabled,
	}

	jobs := jobsvc.NewService(e.db.DB(), e.db, notifier.NewLocalEventNotifier(), nil)
	authSvc := auth.NewService(e.db, cfg.Auth, cfg, jobs, nil)

	e.handler = NewHandler(e.db, cfg, WithSender(e.sender), WithLinkNotifier(authSvc))
}

// linkNotices returns the "Telegram account linked" emails enqueued so far.
func (e *tgEnv) linkNotices(t *testing.T) []*models.Job {
	t.Helper()

	jobs, err := e.db.ListJobs(t.Context(), nil, 0)
	require.NoError(t, err)

	var notices []*models.Job

	for _, job := range jobs {
		if job.Type == string(jobdef.JobTypeEmail) && job.Config["template"] == email.TemplateTelegramLinked {
			notices = append(notices, job)
		}
	}

	return notices
}

func TestLinkNotice_FirstLinkWithLoginEmailsTheOwner(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := setupEnv(t)
	env.withLinkNotice(t, true)

	r.Equal(http.StatusOK, env.post(t, testWebhookSecret, startBody(env.mintToken(t))).Code)
	r.Len(env.telegramContacts(t), 1)

	notices := env.linkNotices(t)
	r.Len(notices, 1, "exactly one link notice")
	r.Equal([]any{env.user.Email}, notices[0].Config["to"])

	data, ok := notices[0].Config["templateData"].(map[string]any)
	r.True(ok, "template data present: %v", notices[0].Config)
	r.Equal("https://solidping.example/d/orgs/"+env.org.Slug+"/account/notifications", data["NotificationsURL"])
}

func TestLinkNotice_ReLinkOfAVerifiedContactSendsNone(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := setupEnv(t)
	env.withLinkNotice(t, true)

	r.Equal(http.StatusOK, env.post(t, testWebhookSecret, startBody(env.mintToken(t))).Code)
	r.Len(env.linkNotices(t), 1)

	r.Equal(http.StatusOK, env.post(t, testWebhookSecret, startBody(env.mintToken(t))).Code)
	r.Len(env.telegramContacts(t), 1)
	r.Len(env.linkNotices(t), 1, "refreshing an already-verified contact sends no notice")
}

// A deleted contact brought back, or one a block un-verified, becomes a
// sign-in method again, so it is announced again.
func TestLinkNotice_RevivedContactIsAnnounced(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		lapse func(ctx context.Context, env *tgEnv, contactUID string) error
	}{
		{"deleted", func(ctx context.Context, env *tgEnv, uid string) error {
			return env.db.DeleteUserContact(ctx, uid)
		}},
		{"unverified by a block", func(ctx context.Context, env *tgEnv, uid string) error {
			return env.db.ClearUserContactVerified(ctx, uid)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			env := setupEnv(t)
			env.withLinkNotice(t, true)
			ctx := t.Context()

			r.Equal(http.StatusOK, env.post(t, testWebhookSecret, startBody(env.mintToken(t))).Code)

			contacts := env.telegramContacts(t)
			r.Len(contacts, 1)
			r.NoError(tc.lapse(ctx, env, contacts[0].UID))

			r.Equal(http.StatusOK, env.post(t, testWebhookSecret, startBody(env.mintToken(t))).Code)
			r.Len(env.linkNotices(t), 2)
		})
	}
}

func TestLinkNotice_NoneWithLoginOff(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	env := setupEnv(t)
	env.withLinkNotice(t, false)

	r.Equal(http.StatusOK, env.post(t, testWebhookSecret, startBody(env.mintToken(t))).Code)
	r.Len(env.telegramContacts(t), 1)
	r.Empty(env.linkNotices(t), "a paging-only link sends no notice")
}
