package jobtypes_test

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
)

// noticeEnv is one wired instance for the operator-notice job: a real
// database, a real job service (so an enqueued email job is observable) and a
// capturing logger — "named in the log" is a requirement of this feature, not
// a debugging aid.
type noticeEnv struct {
	jctx *jobdef.JobContext
	db   db.Service
	org  *models.Organization
	logs *safeBuffer
}

func newNoticeEnv(t *testing.T) *noticeEnv {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	logs := &safeBuffer{}

	return &noticeEnv{
		jctx: &jobdef.JobContext{
			Services: &services.Registry{
				Jobs: jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil),
			},
			DB:        dbSvc.DB(),
			DBService: dbSvc,
			Logger:    slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		},
		db:   dbSvc,
		org:  org,
		logs: logs,
	}
}

// recipient creates a user, joins them to the org and gives them one enabled
// email route — the shape an operator actually has.
func (e *noticeEnv) recipient(t *testing.T, email string, superAdmin, withRoute bool) string {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	user := models.NewUser(email)
	user.SuperAdmin = superAdmin
	r.NoError(e.db.CreateUser(ctx, user))
	r.NoError(e.db.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(e.org.UID, user.UID, models.MemberRoleAdmin)))

	if withRoute {
		r.NoError(e.db.EnsureDefaultEmailRoute(ctx, user.UID, e.org.UID, email))
	}

	return user.UID
}

func (e *noticeEnv) configure(t *testing.T, value map[string]any) {
	t.Helper()

	require.NoError(t, e.db.SetSystemParameter(
		t.Context(), opsnotify.ParamOperatorNotifications, value, false))
}

func (e *noticeEnv) run(t *testing.T, cfg jobtypes.OperatorNoticeJobConfig) error {
	t.Helper()

	raw, err := json.Marshal(cfg)
	require.NoError(t, err)

	runner, err := (&jobtypes.OperatorNoticeJobDefinition{}).CreateJobRun(raw)
	require.NoError(t, err)

	return runner.Run(t.Context(), e.jctx)
}

func (e *noticeEnv) emailJobs(t *testing.T) []*models.Job {
	t.Helper()

	var jobs []*models.Job

	require.NoError(t, e.db.DB().NewSelect().
		Model(&jobs).
		Where("type = ?", string(jobdef.JobTypeEmail)).
		Where("deleted_at IS NULL").
		Scan(t.Context()))

	return jobs
}

func supportNotice() jobtypes.OperatorNoticeJobConfig {
	return jobtypes.OperatorNoticeJobConfig{
		Event:   opsnotify.EventSupportMessage,
		Subject: "[SolidPing support] New support thread on sms from +33600000000",
		Body:    "Channel: sms\nFrom:    +33600000000\n\nis the api down?",
		URL:     "https://solidping.example/dash0/support/thread-1",
	}
}

// TestOperatorNoticeDeliversToSubscribedSuperAdmins is the end-to-end path: a
// configured super admin with an email route receives the notice as a normal
// email job.
func TestOperatorNoticeDeliversToSubscribedSuperAdmins(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newNoticeEnv(t)

	admin := env.recipient(t, "alice@acme.com", true, true)
	env.configure(t, map[string]any{
		"enabled": true,
		"recipients": []map[string]any{
			{"userUid": admin, "events": []string{opsnotify.EventSupportMessage}},
		},
	})

	r.NoError(env.run(t, supportNotice()))

	emails := env.emailJobs(t)
	r.Len(emails, 1)
	r.Contains(jobConfigText(t, emails[0]), "is the api down?")
}

// TestOperatorNoticeSkipsANonSuperAdminRecipient is the authorization contract
// at DELIVERY time: this notice quotes a customer's support message, and
// support threads are RequireSuperAdmin-gated. Somebody who lost the flag since
// the parameter was written must be skipped by name, not delivered to.
func TestOperatorNoticeSkipsANonSuperAdminRecipient(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newNoticeEnv(t)

	demoted := env.recipient(t, "bob@acme.com", false, true)
	env.configure(t, map[string]any{
		"enabled": true,
		"recipients": []map[string]any{
			{"userUid": demoted, "events": []string{opsnotify.EventSupportMessage}},
		},
	})

	r.NoError(env.run(t, supportNotice()), "an ineligible recipient must not fail the run")

	r.Empty(env.emailJobs(t), "a customer's message must never reach a non-super-admin")

	logs := env.logs.String()
	r.Contains(logs, "no longer a super admin")
	r.Contains(logs, "bob@acme.com")
}

// TestOperatorNoticeIsInertWhenDisabled: the master switch is authoritative.
func TestOperatorNoticeIsInertWhenDisabled(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newNoticeEnv(t)

	admin := env.recipient(t, "alice@acme.com", true, true)
	env.configure(t, map[string]any{
		"enabled": false,
		"recipients": []map[string]any{
			{"userUid": admin, "events": []string{opsnotify.EventSupportMessage}},
		},
	})

	r.NoError(env.run(t, supportNotice()))
	r.Empty(env.emailJobs(t))
}

// TestOperatorNoticeHonorsTheEventSubscription: subscribing to signups must not
// deliver support messages.
func TestOperatorNoticeHonorsTheEventSubscription(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newNoticeEnv(t)

	admin := env.recipient(t, "alice@acme.com", true, true)
	env.configure(t, map[string]any{
		"enabled": true,
		"recipients": []map[string]any{
			{"userUid": admin, "events": []string{opsnotify.EventUserRegistered}},
		},
	})

	r.NoError(env.run(t, supportNotice()))
	r.Empty(env.emailJobs(t))
}

// TestOperatorNoticeWarnsWhenEnabledWithoutEligibleRecipients: an operator must
// not be able to believe they are covered when nobody is.
func TestOperatorNoticeWarnsWhenEnabledWithoutEligibleRecipients(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newNoticeEnv(t)

	env.configure(t, map[string]any{"enabled": true})

	r.NoError(env.run(t, supportNotice()))
	r.Empty(env.emailJobs(t))
	r.Contains(env.logs.String(), "no eligible super-admin recipient")
}

// TestOperatorNoticeResolvesTheLandingOrganization proves the reason the org is
// resolved at delivery time rather than by the raiser: at signup the `users`
// row exists before the membership, so only the job can answer "which org?".
func TestOperatorNoticeResolvesTheLandingOrganization(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newNoticeEnv(t)

	admin := env.recipient(t, "alice@acme.com", true, true)
	newcomer := env.recipient(t, "carol@acme.com", false, false)

	env.configure(t, map[string]any{
		"enabled": true,
		"recipients": []map[string]any{
			{"userUid": admin, "events": []string{opsnotify.EventUserRegistered}},
		},
	})

	r.NoError(env.run(t, jobtypes.OperatorNoticeJobConfig{
		Event:        opsnotify.EventUserRegistered,
		Subject:      "[SolidPing] New signup: carol@acme.com (password)",
		Body:         "Email:  carol@acme.com\nMethod: password",
		AboutUserUID: newcomer,
	}))

	emails := env.emailJobs(t)
	r.Len(emails, 1)
	r.Contains(jobConfigText(t, emails[0]), "Org:    acme")
}

// TestOperatorNoticeReportsNoOrganization is the same path for the
// self-registration case that joins nothing — the stuck signup an operator most
// wants to hear about.
func TestOperatorNoticeReportsNoOrganization(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newNoticeEnv(t)
	ctx := t.Context()

	admin := env.recipient(t, "alice@acme.com", true, true)

	orphan := models.NewUser("dave@acme.com")
	r.NoError(env.db.CreateUser(ctx, orphan))

	env.configure(t, map[string]any{
		"enabled": true,
		"recipients": []map[string]any{
			{"userUid": admin, "events": []string{opsnotify.EventUserRegistered}},
		},
	})

	r.NoError(env.run(t, jobtypes.OperatorNoticeJobConfig{
		Event:        opsnotify.EventUserRegistered,
		Subject:      "[SolidPing] New signup: dave@acme.com (password)",
		Body:         "Email:  dave@acme.com\nMethod: password",
		AboutUserUID: orphan.UID,
	}))

	emails := env.emailJobs(t)
	r.Len(emails, 1)
	r.Contains(jobConfigText(t, emails[0]), "no organization")
}

// TestOperatorNoticeNamesAnUnreachableRecipient: a subscriber with no enabled
// route is the likeliest silent failure of the whole feature.
func TestOperatorNoticeNamesAnUnreachableRecipient(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newNoticeEnv(t)

	routeless := env.recipient(t, "bob@acme.com", true, false)
	reachable := env.recipient(t, "alice@acme.com", true, true)

	env.configure(t, map[string]any{
		"enabled": true,
		"recipients": []map[string]any{
			{"userUid": routeless, "events": []string{opsnotify.EventSupportMessage}},
			{"userUid": reachable, "events": []string{opsnotify.EventSupportMessage}},
		},
	})

	r.NoError(env.run(t, supportNotice()), "one unreachable recipient must not fail the run")

	logs := env.logs.String()
	r.Contains(logs, "undeliverable")
	r.Contains(logs, routeless)
	r.Len(env.emailJobs(t), 1, "the reachable recipient still gets the notice")
}

// TestOperatorNoticeRejectsAnEventlessJob: without an event there is no
// subscription to match and no metric label to count under, so the job would be
// an expensive no-op. Fail at construction, where it is visible.
func TestOperatorNoticeRejectsAnEventlessJob(t *testing.T) {
	t.Parallel()

	_, err := (&jobtypes.OperatorNoticeJobDefinition{}).CreateJobRun(
		json.RawMessage(`{"subject":"x"}`))
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "event")
}

// TestOperatorNoticeIsNotPubliclyCreatable: its payload is free text delivered
// to super admins, which must never be reachable from POST /orgs/:org/jobs.
func TestOperatorNoticeIsNotPubliclyCreatable(t *testing.T) {
	t.Parallel()

	require.False(t, jobdef.IsPubliclyCreatable(jobdef.JobTypeOperatorNotice))
}
