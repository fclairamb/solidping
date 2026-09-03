package support_test

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/support"
)

// noticeDispatcherMu serializes the tests that install the process-global
// notice dispatcher. They still declare t.Parallel(); the lock just queues
// them behind one another instead of letting them cross-contaminate.
var noticeDispatcherMu sync.Mutex //nolint:gochecknoglobals // test-local serialization of a process-wide hook

// collector installs a recording dispatcher for the duration of one test.
//
// It filters on a per-test marker (the sender identity) because the dispatcher
// is process-global while the rest of this package's tests run in parallel: an
// unfiltered collector would also record their captures.
type collector struct {
	mu      sync.Mutex
	marker  string
	notices []*opsnotify.Notice
}

func collectNotices(t *testing.T, marker string) *collector {
	t.Helper()

	c := &collector{marker: marker}

	noticeDispatcherMu.Lock()
	t.Cleanup(func() {
		opsnotify.SetDispatcher(nil)
		noticeDispatcherMu.Unlock()
	})

	opsnotify.SetDispatcher(func(_ context.Context, notice *opsnotify.Notice) error {
		if !strings.Contains(notice.Subject, c.marker) {
			return nil
		}

		c.mu.Lock()
		defer c.mu.Unlock()

		c.notices = append(c.notices, notice)

		return nil
	})

	return c
}

func (c *collector) all() []*opsnotify.Notice {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]*opsnotify.Notice(nil), c.notices...)
}

// TestCapture_RaisesOneOperatorNotice: an inbound message produces exactly one
// notice, carrying the channel, the sender, a preview and the thread deep link.
func TestCapture_RaisesOneOperatorNotice(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	notices := collectNotices(t, "+33690000001")
	h := newHarness(t, "support@acme.com")

	thread, _, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel:    models.SupportChannelWhatsApp,
		Identity:   "+33690000001",
		ExternalID: "wamid.AAA",
		Body:       "hey, is the api down for you too?",
	})
	r.NoError(err)

	raised := notices.all()
	r.Len(raised, 1, "one captured message raises exactly one notice")
	r.Equal(opsnotify.EventSupportMessage, raised[0].Event)
	r.Contains(raised[0].Subject, "New support thread")
	r.Contains(raised[0].Subject, models.SupportChannelWhatsApp)
	r.Contains(raised[0].Subject, "+33690000001")
	r.Contains(raised[0].Body, "is the api down for you too?")
	r.Equal("https://solidping.example/dash0/support/"+thread.UID, raised[0].URL)
}

// TestCapture_RaisesTheNoticeWithoutASupportMailbox is the whole point of
// keeping the notice's fold state separate from the mail mirror's: an instance
// with no support mailbox has the mirror OFF, and must still page its
// operators.
func TestCapture_RaisesTheNoticeWithoutASupportMailbox(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	notices := collectNotices(t, "+33690000002")
	h := newHarness(t, "")

	_, _, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelSMS, Identity: "+33690000002",
		ExternalID: "SM-nomailbox", Body: "hello?",
	})
	r.NoError(err)

	r.Zero(h.mailer.count(), "no support mailbox means no mirror mail")
	r.Len(notices.all(), 1, "but the operator notice still goes out")
}

// TestCapture_NoticeFoldWindowCollapsesABurst: a hundred messages in a minute
// must become one notice carrying the count, not a hundred pushes.
func TestCapture_NoticeFoldWindowCollapsesABurst(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	notices := collectNotices(t, "+33690000003")
	h := newHarness(t, "support@acme.com")

	const identity = "+33690000003"

	for i := range 20 {
		_, _, err := h.svc.Capture(t.Context(), &support.Inbound{
			Channel: models.SupportChannelSMS, Identity: identity,
			ExternalID: "SM-burst-" + strconv.Itoa(i), Body: "message " + strconv.Itoa(i),
			At: h.now,
		})
		r.NoError(err)
	}

	r.Len(notices.all(), 1, "a burst inside the fold window is one notice")

	// Past the window the next message is admitted again, and it says how many
	// were swallowed.
	h.now = h.now.Add(support.DefaultMirrorFoldWindow + time.Minute)

	_, _, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelSMS, Identity: identity,
		ExternalID: "SM-burst-after", Body: "still here", At: h.now,
	})
	r.NoError(err)

	raised := notices.all()
	r.Len(raised, 2)
	r.Contains(raised[1].Body, "…and 19 more message(s) in this thread")
}

// TestCapture_MirrorStillCountedAlongsideTheNotice is the POSITIVE CONTROL for
// "the email mirror is untouched": the notice is additive, so the mirror must
// still send and must still be counted in solidping_support_mirror_total.
func TestCapture_MirrorStillCountedAlongsideTheNotice(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	notices := collectNotices(t, "+33690000004")
	h := newHarness(t, "support@acme.com")

	before := testutil.ToFloat64(prommetrics.SupportMirror.WithLabelValues("sent"))

	_, _, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelSMS, Identity: "+33690000004",
		ExternalID: "SM-positive-control", Body: "hello",
	})
	r.NoError(err)

	r.Equal(1, h.mailer.count(), "the mirror mail still goes out")
	r.InDelta(before+1, testutil.ToFloat64(prommetrics.SupportMirror.WithLabelValues("sent")), 0.0001,
		"the mirror is still counted as sent")
	r.Len(notices.all(), 1, "and the notice is additive, not a replacement")
}

// TestCapture_NoticeTruncatesTheQuotedBody: enough to decide whether to drop
// everything and answer, not the whole conversation.
func TestCapture_NoticeTruncatesTheQuotedBody(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	notices := collectNotices(t, "+33690000005")
	h := newHarness(t, "")

	long := strings.Repeat("z", 500)

	_, _, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelSMS, Identity: "+33690000005",
		ExternalID: "SM-long", Body: long,
	})
	r.NoError(err)

	raised := notices.all()
	r.Len(raised, 1)
	r.NotContains(raised[0].Body, long, "the full body never travels")
	r.Contains(raised[0].Body, "…")
}

// TestCapture_DuplicateRaisesNoSecondNotice: providers retry on any non-2xx, so
// a replay is guaranteed. It must not page twice.
func TestCapture_DuplicateRaisesNoSecondNotice(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	notices := collectNotices(t, "+33690000006")
	h := newHarness(t, "")

	inbound := &support.Inbound{
		Channel: models.SupportChannelSMS, Identity: "+33690000006",
		ExternalID: "SM-retry", Body: "hello",
	}

	_, _, err := h.svc.Capture(t.Context(), inbound)
	r.NoError(err)
	_, _, err = h.svc.Capture(t.Context(), inbound)
	r.NoError(err)

	r.Len(notices.all(), 1, "a webhook retry is not a second event")
}

// TestCapture_SurvivesAFailingNoticeDispatcher: capture is the invariant. A
// notice that cannot even be queued must never cost the message.
func TestCapture_SurvivesAFailingNoticeDispatcher(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	noticeDispatcherMu.Lock()
	t.Cleanup(func() {
		opsnotify.SetDispatcher(nil)
		noticeDispatcherMu.Unlock()
	})

	opsnotify.SetDispatcher(func(context.Context, *opsnotify.Notice) error {
		panic("the queue exploded")
	})

	h := newHarness(t, "")

	thread, msg, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelSMS, Identity: "+33690000007",
		ExternalID: "SM-dispatcher-down", Body: "hello",
	})
	r.NoError(err, "a broken notice path must never fail the capture")
	r.NotNil(thread)
	r.NotNil(msg)

	stored, err := h.svc.ListMessages(t.Context(), thread.UID, 10)
	r.NoError(err)
	r.Len(stored, 1, "the message is stored regardless")
}
