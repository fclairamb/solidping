package opsnotifywire_test

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
	"github.com/fclairamb/solidping/server/internal/opsnotifywire"
)

// safeBuffer is a bytes.Buffer usable as a slog destination from a test that
// also reads it.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// wireEnv is a recipient wired with one route of every medium, against a
// deliberately BARE instance: no Telegram token, no web-push keys, no SMS
// resolver, no Slack connection.
type wireEnv struct {
	db      db.Service
	user    *models.User
	log     *slog.Logger
	logs    *safeBuffer
	deps    opsnotify.Deps
	orgUID  string
	nowSeen time.Time
}

func newWireEnv(t *testing.T, registry *services.Registry, cfg *config.Config) *wireEnv {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("alice@acme.com")
	user.SuperAdmin = true
	r.NoError(dbSvc.CreateUser(ctx, user))
	r.NoError(dbSvc.CreateOrganizationMember(ctx,
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	now := time.Now()

	for _, spec := range []struct {
		contactType string
		value       string
		verified    bool
	}{
		{models.UserContactTypeTelegram, "12345", false},
		{models.UserContactTypeSlackUser, "U123", false},
		{models.UserContactTypeWebPush, `{"endpoint":"x"}`, false},
		{models.UserContactTypePhone, "+33600000000", true},
	} {
		contact := models.NewUserContact(user.UID, org.UID, spec.contactType, spec.value, spec.contactType)
		r.NoError(dbSvc.UpsertUserContact(ctx, contact))

		if spec.verified {
			r.NoError(dbSvc.MarkUserContactVerified(ctx, contact.UID, now))
		}

		r.NoError(dbSvc.EnsureUserNotificationRoute(ctx, user.UID, org.UID, contact.UID))
	}

	logs := &safeBuffer{}

	return &wireEnv{
		db:      dbSvc,
		user:    user,
		log:     slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		logs:    logs,
		deps:    opsnotifywire.Build(dbSvc, registry, cfg),
		orgUID:  org.UID,
		nowSeen: now,
	}
}

// TestBuildReportsUnconfiguredMediaAsSkipped is the regression test for a bug
// the transport's own unit tests could not see.
//
// Build assigns all five medium closures unconditionally, so the nil-closure
// branch in opsnotify — the one that means "this instance cannot carry that
// contact type" — is unreachable in a real process. Without the
// ErrMediumUnavailable sentinel, an instance with no Telegram bot token
// therefore recorded outcome="failed" for every Telegram route, making
// "never configured" indistinguishable from "provider is broken" in
// solidping_operator_notice_total — the exact metric added so a silent drop
// stays visible.
//
// This drives the REAL Build output, not a hand-built Deps.
func TestBuildReportsUnconfiguredMediaAsSkipped(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// A bare instance: an empty registry (no job service, no SMS resolver, no
	// web-push keys) and an empty config (no Telegram token).
	env := newWireEnv(t, &services.Registry{}, &config.Config{})

	report := opsnotify.DeliverToUser(t.Context(), env.deps, env.log, env.user.UID, &opsnotify.Notice{
		Event:   opsnotify.EventSupportMessage,
		Subject: "[SolidPing support] New message",
		Body:    "hello",
	})

	r.Zero(report.Delivered)
	r.Equal(4, report.Routes)
	r.Equal(4, report.Skipped,
		"an unconfigured medium is skipped, never counted as a delivery failure")
	r.Zero(report.Failed,
		"nothing here refused the notice; nothing may be reported as having failed")

	logs := env.logs.String()
	for _, medium := range []string{
		models.UserContactTypeTelegram,
		models.UserContactTypeSlackUser,
		models.UserContactTypeWebPush,
		models.UserContactTypePhone,
	} {
		r.Contains(logs, medium+" is not available on this instance",
			"an operator must be able to see WHICH medium is missing")
	}
}

// TestBuildStillDeliversOverAConfiguredMedium is the positive control: the
// skips above must come from the missing configuration, not from Build handing
// back a transport that never sends anything.
func TestBuildStillDeliversOverAConfiguredMedium(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	env := newWireEnv(t, &services.Registry{}, &config.Config{})

	// Email is the medium a stock instance always has, once the job service is
	// wired — which is precisely what an empty registry lacks above.
	r.NoError(env.db.EnsureDefaultEmailRoute(ctx, env.user.UID, env.orgUID, env.user.Email))

	registry := &services.Registry{
		Jobs: jobsvc.NewService(env.db.DB(), env.db, notifier.NewLocalEventNotifier(), nil),
	}
	deps := opsnotifywire.Build(env.db, registry, &config.Config{})

	report := opsnotify.DeliverToUser(ctx, deps, env.log, env.user.UID, &opsnotify.Notice{
		Event:   opsnotify.EventSupportMessage,
		Subject: "[SolidPing support] New message",
		Body:    "hello",
	})

	r.Equal(1, report.Delivered, "the email route must actually carry the notice")
	r.Equal(4, report.Skipped, "the four unconfigured media are still skipped")
	r.Zero(report.Failed)
}

// TestWatchdogDigestKeepsItsOwnLogPrefix: this transport was extracted from the
// platform watchdog and still carries its digests. An operator (or an alert
// rule) grepping for the watchdog must not find its output filed under a name
// that never mentions the watchdog.
func TestWatchdogDigestKeepsItsOwnLogPrefix(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	env := newWireEnv(t, &services.Registry{}, &config.Config{})

	opsnotify.DeliverToUser(t.Context(), env.deps, env.log, env.user.UID, &opsnotify.Notice{
		Event:   opsnotify.EventWatchdogDigest,
		Subject: "Platform watchdog: 1 anomaly",
		Body:    "eu2 is dark",
	})

	logs := env.logs.String()
	r.Contains(logs, "Platform watchdog digest undeliverable")
	r.NotContains(strings.SplitN(logs, "\n", 2)[0], "Operator notice")
}
