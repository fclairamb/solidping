package support_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/support"
)

// Static test errors — err113 forbids inline dynamic errors, including in tests.
var (
	errProviderExploded = errors.New("provider exploded")
	errSMTPDown         = errors.New("smtp is down")
)

// fakeMailer captures what the mirror notification would have sent, and can be
// made to fail on demand.
type fakeMailer struct {
	mu       sync.Mutex
	sent     []*email.Message
	failWith error
}

func (m *fakeMailer) Send(_ context.Context, msg *email.Message) (*email.SendResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.failWith != nil {
		return nil, m.failWith
	}

	m.sent = append(m.sent, msg)

	return &email.SendResult{Sent: true}, nil
}

func (m *fakeMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.sent)
}

func (m *fakeMailer) last() *email.Message {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.sent) == 0 {
		return nil
	}

	return m.sent[len(m.sent)-1]
}

type harness struct {
	svc    *support.Service
	dbSvc  db.Service
	mailer *fakeMailer
	now    time.Time
}

func newHarness(t *testing.T, replyTo string) *harness {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	h := &harness{
		dbSvc:  dbSvc,
		mailer: &fakeMailer{},
		now:    time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC),
	}

	h.svc = support.NewService(dbSvc, support.Options{
		Mailer:  h.mailer,
		BaseURL: "https://solidping.example",
		ReplyTo: replyTo,
		Now:     func() time.Time { return h.now },
	})

	return h
}

// newAdmin creates a real user row so an outbound reply can carry a valid
// author_uid — the column is a foreign key, and a test that passes a fabricated
// uid would only ever exercise the failure path.
func newAdmin(t *testing.T, h *harness) string {
	t.Helper()

	r := require.New(t)
	user := models.NewUser("admin@acme.com")
	r.NoError(h.dbSvc.CreateUser(t.Context(), user))

	return user.UID
}

func TestCapture_CreatesThreadAndMessage(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, "support@acme.com")

	thread, msg, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel:    models.SupportChannelWhatsApp,
		Identity:   "+33600000000",
		ExternalID: "wamid.AAA",
		Body:       "hey, is the api down for you too?",
	})
	r.NoError(err)
	r.NotNil(thread)
	r.NotNil(msg)

	r.Equal(models.SupportStatusOpen, thread.Status)
	r.Equal(models.SupportChannelWhatsApp, thread.Channel)
	r.Equal("+33600000000", thread.ChannelIdentity)
	r.Equal(1, thread.UnreadCount)
	r.NotNil(thread.LastInboundAt)
	r.Contains(thread.Subject, "hey, is the api down")

	// The body and the sender both survive — the whole point of the spec.
	r.Equal("hey, is the api down for you too?", msg.Body)
	r.Equal(models.SupportDirectionInbound, msg.Direction)
	r.False(msg.Truncated)

	messages, err := h.svc.ListMessages(t.Context(), thread.UID, 0)
	r.NoError(err)
	r.Len(messages, 1)
}

func TestCapture_IsIdempotentOnExternalID(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, "")

	in := &support.Inbound{
		Channel:    models.SupportChannelWhatsApp,
		Identity:   "+33600000000",
		ExternalID: "wamid.RETRY",
		Body:       "hello",
	}

	first, msg1, err := h.svc.Capture(t.Context(), in)
	r.NoError(err)

	// Meta and Twilio both retry on any non-2xx, so this is a guaranteed event.
	second, msg2, err := h.svc.Capture(t.Context(), in)
	r.NoError(err)

	r.Equal(first.UID, second.UID)
	r.Equal(msg1.UID, msg2.UID)

	messages, err := h.svc.ListMessages(t.Context(), first.UID, 0)
	r.NoError(err)
	r.Len(messages, 1, "a webhook retry must not double-insert")
}

func TestCapture_ThreadsContinueAndClosureOpensAFreshOne(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, "")

	first, _, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelTelegram, Identity: "12345",
		ExternalID: "12345:1", Body: "first",
	})
	r.NoError(err)

	// A second message continues the SAME conversation.
	second, _, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelTelegram, Identity: "12345",
		ExternalID: "12345:2", Body: "second",
	})
	r.NoError(err)
	r.Equal(first.UID, second.UID)
	r.Equal(2, second.UnreadCount)

	// Close it, then write in again: without the partial unique predicate this
	// identity could never come back.
	closed := models.SupportStatusClosed
	_, err = h.svc.UpdateThread(t.Context(), first.UID, &closed, nil)
	r.NoError(err)

	third, _, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelTelegram, Identity: "12345",
		ExternalID: "12345:3", Body: "back again",
	})
	r.NoError(err)
	r.NotEqual(first.UID, third.UID, "a message after closure must open a fresh thread")
	r.Equal(models.SupportStatusOpen, third.Status)
}

func TestCapture_TruncatesOversizedBody(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, "")

	oversized := make([]rune, models.SupportBodyMaxLength+500)
	for i := range oversized {
		oversized[i] = 'a'
	}

	_, msg, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelSMS, Identity: "+33600000001",
		ExternalID: "SM1", Body: string(oversized),
	})
	r.NoError(err)
	r.True(msg.Truncated)
	r.Len([]rune(msg.Body), models.SupportBodyMaxLength)

	// Positive control: a normal body is not flagged.
	_, small, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelSMS, Identity: "+33600000001",
		ExternalID: "SM2", Body: "short",
	})
	r.NoError(err)
	r.False(small.Truncated)
}

func TestCapture_AttributesVerifiedContactOnly(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	h := newHarness(t, "")

	org := models.NewOrganization("acme", "Acme")
	r.NoError(h.dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("alice@acme.com")
	r.NoError(h.dbSvc.CreateUser(ctx, user))

	unverified := models.NewUserContact(
		user.UID, org.UID, models.UserContactTypeWhatsApp, "+33611111111", "mobile")
	r.NoError(h.dbSvc.UpsertUserContact(ctx, unverified))

	// An UNVERIFIED contact must not attribute: anyone can claim a number.
	unattributed, _, err := h.svc.Capture(ctx, &support.Inbound{
		Channel: models.SupportChannelWhatsApp, Identity: "+33611111111",
		ExternalID: "wamid.U1", Body: "hi",
	})
	r.NoError(err)
	r.Nil(unattributed.UserUID)
	r.Nil(unattributed.OrganizationUID)

	// Positive control: verify it, use a different number, and attribution
	// happens. Without this the assertion above would pass on a service that
	// never attributes at all.
	verified := models.NewUserContact(
		user.UID, org.UID, models.UserContactTypeWhatsApp, "+33622222222", "mobile")
	r.NoError(h.dbSvc.UpsertUserContact(ctx, verified))
	r.NoError(h.dbSvc.MarkUserContactVerified(ctx, verified.UID, time.Now()))

	attributed, _, err := h.svc.Capture(ctx, &support.Inbound{
		Channel: models.SupportChannelWhatsApp, Identity: "+33622222222",
		ExternalID: "wamid.U2", Body: "hi",
	})
	r.NoError(err)
	r.NotNil(attributed.UserUID)
	r.Equal(user.UID, *attributed.UserUID)
	r.NotNil(attributed.OrganizationUID)
	r.Equal(org.UID, *attributed.OrganizationUID)

	// Attribution is a HINT, not ownership: deleting the org must detach it and
	// leave the conversation standing.
	detached, err := h.svc.DetachOrganization(ctx, org.UID)
	r.NoError(err)
	r.Equal(int64(1), detached)

	after, err := h.svc.GetThread(ctx, attributed.UID)
	r.NoError(err)
	r.Nil(after.OrganizationUID)
	r.Nil(after.UserUID)
}

func TestReplyWindow_WhatsAppExpiresAndOthersDoNot(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	inbound := base

	whatsApp := &models.SupportThread{Channel: models.SupportChannelWhatsApp, LastInboundAt: &inbound}

	inside := whatsApp.ReplyWindow(base.Add(23 * time.Hour))
	r.True(inside.Expires)
	r.True(inside.Open)
	r.Empty(inside.Reason)
	r.NotNil(inside.ExpiresAt)
	r.Equal(base.Add(24*time.Hour), *inside.ExpiresAt)

	outside := whatsApp.ReplyWindow(base.Add(24*time.Hour + time.Second))
	r.True(outside.Expires)
	r.False(outside.Open)
	r.Contains(outside.Reason, "24-hour")

	// Exactly at the boundary the window is already closed — Meta's window is
	// half-open, and being optimistic here means failing at send time.
	r.False(whatsApp.ReplyWindow(base.Add(24 * time.Hour)).Open)

	for _, channel := range []string{
		models.SupportChannelTelegram, models.SupportChannelSlack,
		models.SupportChannelDiscord, models.SupportChannelSMS,
	} {
		thread := &models.SupportThread{Channel: channel, LastInboundAt: &inbound}
		window := thread.ReplyWindow(base.Add(100 * 24 * time.Hour))
		r.False(window.Expires, "%s must not expire", channel)
		r.True(window.Open, "%s must stay repliable", channel)
	}

	// SMS never expires but costs money on every reply.
	sms := &models.SupportThread{Channel: models.SupportChannelSMS, LastInboundAt: &inbound}
	r.True(sms.ReplyWindow(base).CostsMoney)
	r.False((&models.SupportThread{Channel: models.SupportChannelTelegram}).ReplyWindow(base).CostsMoney)
}

func TestReply_RefusesOutsideTheWhatsAppWindow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, "")

	sent := 0
	h.svc.RegisterReplier(models.SupportChannelWhatsApp,
		func(_ context.Context, _ *models.SupportThread, _ string) (string, error) {
			sent++

			return "wamid.OUT", nil
		})

	thread, _, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelWhatsApp, Identity: "+33600000000",
		ExternalID: "wamid.IN", Body: "help",
	})
	r.NoError(err)

	// Inside the window: the reply goes.
	admin := newAdmin(t, h)

	msg, err := h.svc.Reply(t.Context(), thread.UID, "on it", admin)
	r.NoError(err)
	r.Equal(models.SupportDirectionOutbound, msg.Direction)
	r.NotNil(msg.AuthorUID)
	r.Equal(admin, *msg.AuthorUID)
	r.Equal(1, sent)

	// Outside it: refused BY US, with a reason, and the provider is never called.
	h.now = h.now.Add(25 * time.Hour)

	_, err = h.svc.Reply(t.Context(), thread.UID, "still there?", admin)
	r.ErrorIs(err, support.ErrReplyWindowClosed)
	r.Contains(err.Error(), "24-hour")
	r.Equal(1, sent, "an expired window must not reach the provider at all")
}

func TestReply_RecordsAFailedSend(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, "")

	sendErr := errProviderExploded
	h.svc.RegisterReplier(models.SupportChannelTelegram,
		func(_ context.Context, _ *models.SupportThread, _ string) (string, error) {
			return "", sendErr
		})

	thread, _, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelTelegram, Identity: "999", ExternalID: "999:1", Body: "hi",
	})
	r.NoError(err)

	msg, err := h.svc.Reply(t.Context(), thread.UID, "hello back", newAdmin(t, h))
	r.Error(err)
	r.NotNil(msg, "a failed reply must still leave a trace")
	r.Equal("failed", msg.Delivery["status"])

	messages, err := h.svc.ListMessages(t.Context(), thread.UID, 0)
	r.NoError(err)
	r.Len(messages, 2)
}

func TestReply_NoAdapterIsRefusedNotCrashed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, "")

	thread, _, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelDiscord, Identity: "d1", ExternalID: "d1:1", Body: "hi",
	})
	r.NoError(err)

	route := h.svc.ReplyRouteFor(t.Context(), thread)
	r.False(route.CanReply)
	r.Contains(route.Reason, "no reply adapter")

	_, err = h.svc.Reply(t.Context(), thread.UID, "hello", "")
	r.ErrorIs(err, support.ErrNoReplier)
}

func TestMirror_MarkersThrottleAndFailureIsolation(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, "support@acme.com")

	thread, _, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelWhatsApp, Identity: "+33600000000",
		ExternalID: "wamid.M1", Body: "hello there",
	})
	r.NoError(err)
	r.Equal(1, h.mailer.count())

	mail := h.mailer.last()
	r.True(mail.AutoSubmitted, "RFC 3834 marker missing")
	r.True(mail.SupportMirror, "X-SolidPing-Support-Mirror marker missing")
	r.False(mail.SupportReplyable, "a mirror must not point its Reply-To at itself")
	r.Equal([]string{"support@acme.com"}, mail.Recipients.To)
	// It says plainly that it is a notification, and leads with the thread link.
	r.Contains(mail.Text, "not a conversation")
	r.Contains(mail.Text, "https://solidping.example/dash0/support/"+thread.UID)
	r.Contains(mail.Text, "hello there")

	// A burst folds into the first mail rather than producing one per message.
	for i := range 5 {
		_, _, burstErr := h.svc.Capture(t.Context(), &support.Inbound{
			Channel: models.SupportChannelWhatsApp, Identity: "+33600000000",
			ExternalID: "wamid.burst" + string(rune('a'+i)), Body: "again",
		})
		r.NoError(burstErr)
	}

	r.Equal(1, h.mailer.count(), "a burst must collapse into one mirror")

	// Past the fold window a new mirror goes out, and reports what was folded.
	h.now = h.now.Add(support.DefaultMirrorFoldWindow + time.Minute)

	_, _, err = h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelWhatsApp, Identity: "+33600000000",
		ExternalID: "wamid.after", Body: "still here",
	})
	r.NoError(err)
	r.Equal(2, h.mailer.count())
	r.Contains(h.mailer.last().Text, "folded")
}

func TestMirror_FailureLeavesTheMessageCaptured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, "support@acme.com")
	h.mailer.failWith = errSMTPDown

	thread, msg, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelSMS, Identity: "+33600000002",
		ExternalID: "SMX", Body: "anyone there?",
	})
	r.NoError(err, "a bounced notification is a smaller problem than a lost message")
	r.NotNil(msg)

	stored, err := h.svc.ListMessages(t.Context(), thread.UID, 0)
	r.NoError(err)
	r.Len(stored, 1)
	r.Equal("anyone there?", stored[0].Body)
}

func TestMirror_DisabledWithoutASupportMailbox(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, "")

	_, _, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelSMS, Identity: "+33600000003",
		ExternalID: "SMY", Body: "hi",
	})
	r.NoError(err)
	r.Equal(0, h.mailer.count(), "no support mailbox means no mirror at all")
}

func TestPurgeClosedBefore_OnlyTouchesOldClosedThreads(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	h := newHarness(t, "")

	open, _, err := h.svc.Capture(ctx, &support.Inbound{
		Channel: models.SupportChannelTelegram, Identity: "open-1",
		ExternalID: "o1", Body: "still talking",
	})
	r.NoError(err)

	stale, _, err := h.svc.Capture(ctx, &support.Inbound{
		Channel: models.SupportChannelTelegram, Identity: "closed-1",
		ExternalID: "c1", Body: "resolved long ago",
	})
	r.NoError(err)

	closed := models.SupportStatusClosed
	_, err = h.svc.UpdateThread(ctx, stale.UID, &closed, nil)
	r.NoError(err)

	// Nothing is due yet.
	purged, err := h.svc.PurgeClosedBefore(ctx, h.now.Add(-24*time.Hour), 100)
	r.NoError(err)
	r.Equal(int64(0), purged)

	// Past the retention horizon the closed thread goes, and the open one stays.
	purged, err = h.svc.PurgeClosedBefore(ctx, h.now.Add(time.Hour), 100)
	r.NoError(err)
	r.Equal(int64(1), purged)

	_, err = h.svc.GetThread(ctx, stale.UID)
	r.ErrorIs(err, support.ErrThreadNotFound)

	survivor, err := h.svc.GetThread(ctx, open.UID)
	r.NoError(err)
	r.Equal(open.UID, survivor.UID)

	// The bodies go with the thread — that is the point of a retention period
	// on personal data.
	messages, err := h.svc.ListMessages(ctx, stale.UID, 0)
	r.NoError(err)
	r.Empty(messages)
}

func TestListThreads_FiltersAndSearch(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	h := newHarness(t, "")

	_, _, err := h.svc.Capture(ctx, &support.Inbound{
		Channel: models.SupportChannelWhatsApp, Identity: "+33600000004",
		ExternalID: "w1", Body: "database is slow",
	})
	r.NoError(err)

	h.now = h.now.Add(time.Minute)

	_, _, err = h.svc.Capture(ctx, &support.Inbound{
		Channel: models.SupportChannelTelegram, Identity: "77",
		ExternalID: "t1", Body: "hello",
	})
	r.NoError(err)

	all, err := h.svc.ListThreads(ctx, models.ListSupportThreadsFilter{})
	r.NoError(err)
	r.Len(all, 2)
	r.Equal(models.SupportChannelTelegram, all[0].Channel, "newest activity first")

	byChannel, err := h.svc.ListThreads(ctx,
		models.ListSupportThreadsFilter{Channel: models.SupportChannelWhatsApp})
	r.NoError(err)
	r.Len(byChannel, 1)

	bySearch, err := h.svc.ListThreads(ctx, models.ListSupportThreadsFilter{Query: "DATABASE"})
	r.NoError(err)
	r.Len(bySearch, 1, "search must be case-insensitive")
}

// TestRecordDelivery_UpdatesAnOutboundReply proves the outbound reply's
// delivery status is driven by the SAME provider callbacks that already update
// incident_notifications, rather than a second pipeline.
func TestRecordDelivery_UpdatesAnOutboundReply(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	h := newHarness(t, "")

	h.svc.RegisterReplier(models.SupportChannelWhatsApp,
		func(_ context.Context, _ *models.SupportThread, _ string) (string, error) {
			return "wamid.OUT1", nil
		})

	thread, inbound, err := h.svc.Capture(ctx, &support.Inbound{
		Channel: models.SupportChannelWhatsApp, Identity: "+33600000000",
		ExternalID: "wamid.IN1", Body: "hello",
	})
	r.NoError(err)

	reply, err := h.svc.Reply(ctx, thread.UID, "on it", "")
	r.NoError(err)
	r.Equal("sent", reply.Delivery["status"])

	h.svc.RecordDelivery(ctx, models.SupportChannelWhatsApp, "wamid.OUT1", "delivered")

	messages, err := h.svc.ListMessages(ctx, thread.UID, 0)
	r.NoError(err)
	r.Len(messages, 2)

	var outbound *models.SupportMessage

	for _, msg := range messages {
		if msg.Direction == models.SupportDirectionOutbound {
			outbound = msg
		}
	}

	r.NotNil(outbound)
	r.Equal("delivered", outbound.Delivery["status"])

	// The INBOUND message is untouched: a delivery receipt is about what WE
	// sent, and stamping it on what they sent would be nonsense.
	for _, msg := range messages {
		if msg.UID == inbound.UID {
			r.Empty(msg.Delivery, "an inbound message carries no delivery status")
		}
	}

	// An unknown provider id is the normal case — the overwhelming majority of
	// these callbacks are about an alert, not a support reply — and must be a
	// silent no-op rather than an error.
	r.NotPanics(func() {
		h.svc.RecordDelivery(ctx, models.SupportChannelWhatsApp, "wamid.NOTOURS", "delivered")
	})
}

// TestCapture_AbuseCeilingsAreEnforced pins the two abuse caps. These endpoints
// are fed by publicly reachable phone numbers, so the tables are
// attacker-influenced and the ceilings are hard bounds, not niceties.
func TestCapture_AbuseCeilingsAreEnforced(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	h := newHarness(t, "")

	// One identity may not open unlimited NEW threads in a day: open, close,
	// repeat is legitimate; doing it fifty times is not.
	var lastErr error

	for i := range support.DefaultThreadsPerIdentityPerDay + 2 {
		thread, _, err := h.svc.Capture(ctx, &support.Inbound{
			Channel: models.SupportChannelSMS, Identity: "+33699999999",
			ExternalID: "SM-flood-" + strconv.Itoa(i), Body: "hi",
		})
		if err != nil {
			lastErr = err

			break
		}

		closed := models.SupportStatusClosed
		_, err = h.svc.UpdateThread(ctx, thread.UID, &closed, nil)
		r.NoError(err)
	}

	r.ErrorIs(lastErr, support.ErrTooManyThreads)

	// The ceiling is per identity: a different sender is unaffected, so a flood
	// from one number cannot lock everybody else out.
	_, _, err := h.svc.Capture(ctx, &support.Inbound{
		Channel: models.SupportChannelSMS, Identity: "+33688888888",
		ExternalID: "SM-other", Body: "hi",
	})
	r.NoError(err)
}
