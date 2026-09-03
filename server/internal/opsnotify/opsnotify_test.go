package opsnotify_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
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

// sent is one delivery the fake transport accepted.
type sent struct {
	medium  string
	orgUID  string
	target  string
	subject string
	body    string
}

// env is one wired instance: a real sqlite database, a capturing logger, and a
// Deps whose media record instead of sending.
type env struct {
	db   db.Service
	log  *slog.Logger
	logs *safeBuffer

	mu   sync.Mutex
	sent []sent
	// failing media report an error rather than accepting.
	failing map[string]bool
	// missing media are left nil on Deps — "this instance cannot carry it".
	missing map[string]bool
}

func newEnv(t *testing.T) *env {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	logs := &safeBuffer{}

	return &env{
		db:      dbSvc,
		log:     slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		logs:    logs,
		failing: map[string]bool{},
		missing: map[string]bool{},
	}
}

var errMediumRefused = errors.New("medium refused the notice")

func (e *env) record(medium, orgUID, target, subject, body string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.failing[medium] {
		return errMediumRefused
	}

	e.sent = append(e.sent, sent{medium: medium, orgUID: orgUID, target: target, subject: subject, body: body})

	return nil
}

func (e *env) deliveries() []sent {
	e.mu.Lock()
	defer e.mu.Unlock()

	return append([]sent(nil), e.sent...)
}

func (e *env) deps() opsnotify.Deps {
	deps := opsnotify.Deps{DB: e.db}

	if !e.missing["email"] {
		deps.EnqueueEmail = func(_ context.Context, orgUID, to, subject, text string) error {
			return e.record("email", orgUID, to, subject, text)
		}
	}

	if !e.missing["telegram"] {
		deps.SendTelegram = func(_ context.Context, chatID, html string) error {
			return e.record("telegram", "", chatID, "", html)
		}
	}

	if !e.missing["slack"] {
		deps.SendSlackDM = func(_ context.Context, orgUID, user, text string) error {
			return e.record("slack", orgUID, user, "", text)
		}
	}

	if !e.missing["webpush"] {
		deps.SendWebPush = func(_ context.Context, sub, title, body, url string) error {
			return e.record("webpush", "", sub, title, body+"|"+url)
		}
	}

	if !e.missing["sms"] {
		deps.SendSMS = func(_ context.Context, orgUID, to, body string) error {
			return e.record("sms", orgUID, to, "", body)
		}
	}

	return deps
}

func (e *env) org(t *testing.T, slug string) *models.Organization {
	t.Helper()

	org := models.NewOrganization(slug, strings.ToUpper(slug))
	require.NoError(t, e.db.CreateOrganization(t.Context(), org))

	return org
}

func (e *env) user(t *testing.T, email string, superAdmin bool) *models.User {
	t.Helper()

	user := models.NewUser(email)
	user.SuperAdmin = superAdmin
	require.NoError(t, e.db.CreateUser(t.Context(), user))

	return user
}

func (e *env) join(t *testing.T, org *models.Organization, user *models.User) {
	t.Helper()

	require.NoError(t, e.db.CreateOrganizationMember(t.Context(),
		models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))
}

// contact adds a contact of an arbitrary type plus its enabled route.
func (e *env) contact(
	t *testing.T, org *models.Organization, user *models.User, contactType, value string, verified bool,
) *models.UserContact {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	contact := models.NewUserContact(user.UID, org.UID, contactType, value, contactType)
	r.NoError(e.db.UpsertUserContact(ctx, contact))

	if verified {
		r.NoError(e.db.MarkUserContactVerified(ctx, contact.UID, time.Now()))
	}

	r.NoError(e.db.EnsureUserNotificationRoute(ctx, user.UID, org.UID, contact.UID))

	return contact
}

func testNotice() opsnotify.Notice {
	return opsnotify.Notice{
		Event:   opsnotify.EventSupportMessage,
		Subject: "[SolidPing support] New message on sms from +33600000000",
		Body:    "Channel: sms\nFrom:    +33600000000\n\nis the api down for you too?",
		URL:     "https://solidping.example/dash0/support/thread-1",
	}
}

// TestDeliverDedupsDestinationsAcrossOrgs: user_contacts are org-scoped, so an
// operator in two orgs has two rows for one address. They are one human.
func TestDeliverDedupsDestinationsAcrossOrgs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	ctx := t.Context()

	const email = "alice@acme.com"

	alice := e.user(t, email, true)
	first := e.org(t, "acme")
	second := e.org(t, "acme-eu")
	e.join(t, first, alice)
	e.join(t, second, alice)

	r.NoError(e.db.EnsureDefaultEmailRoute(ctx, alice.UID, first.UID, email))
	// Same address, different case, second org: a different contacts row.
	r.NoError(e.db.EnsureDefaultEmailRoute(ctx, alice.UID, second.UID, strings.ToUpper(email)))

	report := opsnotify.DeliverToUser(ctx, e.deps(), e.log, alice.UID, testNotice())

	r.Equal(1, report.Delivered, "one human with one address gets one notice, not two")
	r.Len(e.deliveries(), 1)
}

// TestDeliverWarnsWhenTheRecipientHasNoRoute: the likeliest silent failure of
// the whole feature. It must be NAMED in the log, never merely counted.
func TestDeliverWarnsWhenTheRecipientHasNoRoute(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	bob := e.user(t, "bob@acme.com", true)
	e.join(t, e.org(t, "acme"), bob)

	report := opsnotify.DeliverToUser(t.Context(), e.deps(), e.log, bob.UID, testNotice())

	r.True(report.Undeliverable())
	r.Zero(report.Delivered)

	logs := e.logs.String()
	r.Contains(logs, "undeliverable")
	r.Contains(logs, bob.UID)
	r.Contains(logs, "bob@acme.com")
}

// TestDeliverWarnsWhenTheRecipientDoesNotExist keeps a stale configuration from
// failing silently.
func TestDeliverWarnsWhenTheRecipientDoesNotExist(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	report := opsnotify.DeliverToUser(t.Context(), e.deps(), e.log, "does-not-exist", testNotice())

	r.True(report.Undeliverable())
	r.Contains(e.logs.String(), "undeliverable")
}

// TestDeliverRendersEveryContactType pins the per-medium rendering the
// transport inherited from the watchdog: escaped Telegram HTML, a plain-text
// Slack DM, a push title plus first content line, and an SMS.
func TestDeliverRendersEveryContactType(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	alice := e.user(t, "alice@acme.com", true)
	org := e.org(t, "acme")
	e.join(t, org, alice)

	e.contact(t, org, alice, models.UserContactTypeEmail, "alice@acme.com", false)
	e.contact(t, org, alice, models.UserContactTypeTelegram, "12345", false)
	e.contact(t, org, alice, models.UserContactTypeSlackUser, "U123", false)
	e.contact(t, org, alice, models.UserContactTypeWebPush, `{"endpoint":"x"}`, false)
	e.contact(t, org, alice, models.UserContactTypePhone, "+33600000000", true)

	notice := testNotice()
	report := opsnotify.DeliverToUser(t.Context(), e.deps(), e.log, alice.UID, notice)

	r.Equal(5, report.Delivered)

	byMedium := map[string]sent{}
	for _, delivery := range e.deliveries() {
		byMedium[delivery.medium] = delivery
	}

	r.Equal(notice.Subject, byMedium["email"].subject)
	r.Contains(byMedium["email"].body, notice.URL, "the email carries the deep link")

	r.Contains(byMedium["telegram"].body, "<b>", "the telegram body is HTML")
	r.NotContains(byMedium["telegram"].body, "<script", "nothing untrusted is interpolated raw")

	r.Contains(byMedium["slack"].body, "is the api down for you too?")

	r.Equal(notice.Subject, byMedium["webpush"].subject)
	r.Contains(byMedium["webpush"].body, "Channel: sms", "the push body is the first content line")
	r.Contains(byMedium["webpush"].body, notice.URL, "the push carries the click-through URL")

	r.Contains(byMedium["sms"].body, notice.Subject)
}

// TestDeliverEscapesTelegramMarkup: support bodies are typed by a stranger, so
// a crafted message must not become markup in an operator's Telegram client.
func TestDeliverEscapesTelegramMarkup(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	alice := e.user(t, "alice@acme.com", true)
	org := e.org(t, "acme")
	e.join(t, org, alice)
	e.contact(t, org, alice, models.UserContactTypeTelegram, "12345", false)

	notice := testNotice()
	notice.Body = `<a href="https://evil.example">click me</a>`

	opsnotify.DeliverToUser(t.Context(), e.deps(), e.log, alice.UID, notice)

	deliveries := e.deliveries()
	r.Len(deliveries, 1)
	r.NotContains(deliveries[0].body, `<a href="https://evil.example">`)
	r.Contains(deliveries[0].body, "&lt;a href=")
}

// TestDeliverTruncatesSMS: an SMS is not a report. 300 characters plus the
// ellipsis and the opt-out footer, never the whole digest.
func TestDeliverTruncatesSMS(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	alice := e.user(t, "alice@acme.com", true)
	org := e.org(t, "acme")
	e.join(t, org, alice)
	e.contact(t, org, alice, models.UserContactTypePhone, "+33600000000", true)

	notice := testNotice()
	notice.Body = strings.Repeat("a very long support message ", 40)

	opsnotify.DeliverToUser(t.Context(), e.deps(), e.log, alice.UID, notice)

	deliveries := e.deliveries()
	r.Len(deliveries, 1)
	r.Contains(deliveries[0].body, "…", "an over-long body is truncated, not sent whole")
	r.Less(len(deliveries[0].body), 400)
}

// TestDeliverSkipsUnverifiedPhone: an unverified number is never contacted —
// the same rule escalation paging follows.
func TestDeliverSkipsUnverifiedPhone(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	alice := e.user(t, "alice@acme.com", true)
	org := e.org(t, "acme")
	e.join(t, org, alice)
	e.contact(t, org, alice, models.UserContactTypePhone, "+33600000000", false)

	report := opsnotify.DeliverToUser(t.Context(), e.deps(), e.log, alice.UID, testNotice())

	r.Zero(report.Delivered)
	r.Equal(1, report.Skipped)
	r.Empty(e.deliveries())
	r.Contains(e.logs.String(), "not verified")
}

// TestDeliverSkipsWhatsApp: Meta will not carry free-form business text
// outside a session, so the route is NAMED in the log rather than silently
// dropped — an operator whose only route is WhatsApp must be able to find out.
func TestDeliverSkipsWhatsApp(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	alice := e.user(t, "alice@acme.com", true)
	org := e.org(t, "acme")
	e.join(t, org, alice)
	e.contact(t, org, alice, models.UserContactTypeWhatsApp, "+33600000000", true)

	report := opsnotify.DeliverToUser(t.Context(), e.deps(), e.log, alice.UID, testNotice())

	r.Zero(report.Delivered)
	r.Equal(1, report.Skipped)
	r.Empty(e.deliveries())
	r.Contains(e.logs.String(), models.UserContactTypeWhatsApp)
}

// TestDeliverSurvivesOneFailingMedium: a dead route must never abort the
// others, and must still be counted.
func TestDeliverSurvivesOneFailingMedium(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	e.failing["telegram"] = true

	alice := e.user(t, "alice@acme.com", true)
	org := e.org(t, "acme")
	e.join(t, org, alice)
	e.contact(t, org, alice, models.UserContactTypeTelegram, "12345", false)
	e.contact(t, org, alice, models.UserContactTypeEmail, "alice@acme.com", false)

	report := opsnotify.DeliverToUser(t.Context(), e.deps(), e.log, alice.UID, testNotice())

	r.Equal(1, report.Delivered)
	r.Equal(1, report.Failed)
	r.Len(e.deliveries(), 1)
}

// TestDeliverWarnsWhenAMediumIsNotConfigured: "telegram is not set up on this
// instance" is a configuration problem an operator must be able to see.
func TestDeliverWarnsWhenAMediumIsNotConfigured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	e.missing["telegram"] = true

	alice := e.user(t, "alice@acme.com", true)
	org := e.org(t, "acme")
	e.join(t, org, alice)
	e.contact(t, org, alice, models.UserContactTypeTelegram, "12345", false)

	report := opsnotify.DeliverToUser(t.Context(), e.deps(), e.log, alice.UID, testNotice())

	r.Zero(report.Delivered)
	r.Equal(1, report.Skipped)
	r.Contains(e.logs.String(), "Telegram not configured")
}

// TestResolveRecipientsRequiresSuperAdminAtDeliveryTime is the authorization
// contract. A notice can quote a customer's support message, and support
// threads are RequireSuperAdmin-gated: someone who lost the flag since the
// configuration was written must be skipped, by name, not delivered to.
func TestResolveRecipientsRequiresSuperAdminAtDeliveryTime(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	admin := e.user(t, "alice@acme.com", true)
	demoted := e.user(t, "bob@acme.com", false)

	cfg := &opsnotify.Config{
		Enabled: true,
		Recipients: []opsnotify.Recipient{
			{UserUID: admin.UID, Events: []string{opsnotify.EventSupportMessage}},
			{UserUID: demoted.UID, Events: []string{opsnotify.EventSupportMessage}},
			{UserUID: "ghost", Events: []string{opsnotify.EventSupportMessage}},
		},
	}

	resolved := opsnotify.ResolveRecipients(
		t.Context(), e.db, e.log, cfg, opsnotify.EventSupportMessage)

	r.Equal([]string{admin.UID}, resolved)

	logs := e.logs.String()
	r.Contains(logs, "no longer a super admin")
	r.Contains(logs, "bob@acme.com", "the skipped recipient is named")
	r.Contains(logs, "no longer exists")
}

// TestResolveRecipientsHonorsTheEventSubscription: subscribing to one event
// must not subscribe to the other.
func TestResolveRecipientsHonorsTheEventSubscription(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	admin := e.user(t, "alice@acme.com", true)

	cfg := &opsnotify.Config{
		Enabled:    true,
		Recipients: []opsnotify.Recipient{{UserUID: admin.UID, Events: []string{opsnotify.EventUserRegistered}}},
	}

	r.Empty(opsnotify.ResolveRecipients(t.Context(), e.db, e.log, cfg, opsnotify.EventSupportMessage))
	r.Len(opsnotify.ResolveRecipients(t.Context(), e.db, e.log, cfg, opsnotify.EventUserRegistered), 1)
}

// TestResolveRecipientsIsEmptyWhenDisabled: the master switch is authoritative.
func TestResolveRecipientsIsEmptyWhenDisabled(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	admin := e.user(t, "alice@acme.com", true)

	cfg := &opsnotify.Config{
		Enabled:    false,
		Recipients: []opsnotify.Recipient{{UserUID: admin.UID, Events: []string{opsnotify.EventSupportMessage}}},
	}

	r.Empty(opsnotify.ResolveRecipients(t.Context(), e.db, e.log, cfg, opsnotify.EventSupportMessage))
}

// TestLoadConfigDefaultsToDisabled: an instance that never wrote the parameter
// must not start messaging whoever happens to be user #1.
func TestLoadConfigDefaultsToDisabled(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)

	cfg, err := opsnotify.LoadConfig(t.Context(), e.db)
	r.NoError(err)
	r.False(cfg.Enabled)
	r.Empty(cfg.Recipients)
}

// TestLoadConfigRoundTrips proves the parameter the API writes is the one the
// job reads.
func TestLoadConfigRoundTrips(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	ctx := t.Context()

	r.NoError(e.db.SetSystemParameter(ctx, opsnotify.ParamOperatorNotifications, map[string]any{
		"enabled": true,
		"recipients": []map[string]any{
			{"userUid": "uid-1", "events": []string{opsnotify.EventSupportMessage}},
		},
	}, false))

	cfg, err := opsnotify.LoadConfig(ctx, e.db)
	r.NoError(err)
	r.True(cfg.Enabled)
	r.Equal([]string{"uid-1"}, cfg.RecipientsFor(opsnotify.EventSupportMessage))
	r.Empty(cfg.RecipientsFor(opsnotify.EventUserRegistered))
}

// TestValidateParameter is the write-time guard. A typo in an event name would
// otherwise silently subscribe an operator to nothing at all, which is the
// exact failure this feature exists to remove.
func TestValidateParameter(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		value   any
		wantErr error
	}{
		{"empty object", map[string]any{}, nil},
		{
			"minimal valid",
			map[string]any{"enabled": true, "recipients": []map[string]any{
				{"userUid": "uid-1", "events": []string{opsnotify.EventSupportMessage}},
			}},
			nil,
		},
		{
			"unknown event",
			map[string]any{"recipients": []map[string]any{
				{"userUid": "uid-1", "events": []string{"support.mesage"}},
			}},
			opsnotify.ErrInvalidEvent,
		},
		{
			"blank recipient",
			map[string]any{"recipients": []map[string]any{
				{"userUid": "  ", "events": []string{opsnotify.EventSupportMessage}},
			}},
			opsnotify.ErrInvalidRecipient,
		},
		{
			"no events",
			map[string]any{"recipients": []map[string]any{{"userUid": "uid-1", "events": []string{}}}},
			opsnotify.ErrNoEvents,
		},
		{
			"duplicate recipient",
			map[string]any{"recipients": []map[string]any{
				{"userUid": "uid-1", "events": []string{opsnotify.EventSupportMessage}},
				{"userUid": "uid-1", "events": []string{opsnotify.EventUserRegistered}},
			}},
			opsnotify.ErrDuplicateRecipient,
		},
		{"not an object", "enabled", opsnotify.ErrInvalidParameterShape},
		{
			"recipients not a list",
			map[string]any{"recipients": "uid-1"},
			opsnotify.ErrInvalidParameterShape,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := opsnotify.ValidateParameter(testCase.value)
			if testCase.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, testCase.wantErr)
		})
	}
}

// TestValidateParameterWithDBRejectsNonSuperAdmin: the write-time half of the
// authorization rule. Naming a regular user must fail the save, not silently
// produce a recipient the delivery path will skip forever.
func TestValidateParameterWithDBRejectsNonSuperAdmin(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	e := newEnv(t)
	ctx := t.Context()

	admin := e.user(t, "alice@acme.com", true)
	regular := e.user(t, "bob@acme.com", false)

	ok := map[string]any{"enabled": true, "recipients": []map[string]any{
		{"userUid": admin.UID, "events": []string{opsnotify.EventSupportMessage}},
	}}
	r.NoError(opsnotify.ValidateParameterWithDB(ctx, e.db, ok))

	bad := map[string]any{"enabled": true, "recipients": []map[string]any{
		{"userUid": regular.UID, "events": []string{opsnotify.EventSupportMessage}},
	}}
	r.ErrorIs(opsnotify.ValidateParameterWithDB(ctx, e.db, bad), opsnotify.ErrNotSuperAdmin)

	ghost := map[string]any{"enabled": true, "recipients": []map[string]any{
		{"userUid": "nobody", "events": []string{opsnotify.EventSupportMessage}},
	}}
	r.ErrorIs(opsnotify.ValidateParameterWithDB(ctx, e.db, ghost), opsnotify.ErrInvalidRecipient)
}

// TestNotifyNeverFailsTheCaller: the two raisers are a support webhook under a
// provider deadline and a completing signup. Neither may fail because the
// queue is unhappy, so a dispatcher that panics or errors must be absorbed.
func TestNotifyNeverFailsTheCaller(t *testing.T) {
	t.Parallel()

	dispatcherMu.Lock()
	t.Cleanup(func() {
		opsnotify.SetDispatcher(nil)
		dispatcherMu.Unlock()
	})

	opsnotify.SetDispatcher(func(context.Context, opsnotify.Notice) error {
		return errMediumRefused
	})
	require.NotPanics(t, func() { opsnotify.Notify(t.Context(), testNotice()) })

	opsnotify.SetDispatcher(func(context.Context, opsnotify.Notice) error {
		panic("the queue exploded")
	})
	require.NotPanics(t, func() { opsnotify.Notify(t.Context(), testNotice()) })

	opsnotify.SetDispatcher(nil)
	require.NotPanics(t, func() { opsnotify.Notify(t.Context(), testNotice()) })
}

// dispatcherMu serializes the tests that install a process-global dispatcher.
// They still run under t.Parallel(); the lock just queues them.
var dispatcherMu sync.Mutex //nolint:gochecknoglobals // test-local serialization of a process-wide hook
