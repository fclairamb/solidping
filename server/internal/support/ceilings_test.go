package support_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/support"
)

// TestCapture_MessagesPerThreadCeiling covers DefaultMessagesPerThreadPerHour,
// the sibling of the per-identity thread cap.
//
// These endpoints are fed by publicly reachable phone numbers, so one
// conversation flooding the table is not hypothetical. Over the ceiling the
// message is DROPPED, not stored — but the thread is still returned and the
// webhook still succeeds, because losing a message must never cost the channel.
func TestCapture_MessagesPerThreadCeiling(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	// No support mailbox: this test is about the capture ceiling, and mirrors
	// would only add noise (they are folded anyway, being one thread).
	h := newHarness(t, "")

	const identity = "+33612340000"

	var thread *models.SupportThread

	// Everything up to the ceiling is captured.
	for i := range support.DefaultMessagesPerThreadPerHour {
		captured, msg, err := h.svc.Capture(ctx, &support.Inbound{
			Channel: models.SupportChannelSMS, Identity: identity,
			ExternalID: "SM-cap-" + strconv.Itoa(i), Body: "message " + strconv.Itoa(i),
		})
		r.NoError(err)
		r.NotNil(msg, "message %d is below the ceiling and must be captured", i)

		thread = captured
	}

	stored, err := h.svc.ListMessages(ctx, thread.UID, 1000)
	r.NoError(err)
	r.Len(stored, support.DefaultMessagesPerThreadPerHour,
		"every message below the ceiling must be stored")

	// One past it: dropped, but not an error and not a lost thread.
	over, msg, err := h.svc.Capture(ctx, &support.Inbound{
		Channel: models.SupportChannelSMS, Identity: identity,
		ExternalID: "SM-cap-over", Body: "one too many",
	})
	r.NoError(err, "an abuse ceiling must not fail the webhook")
	r.Nil(msg, "past the hourly ceiling the message is dropped")
	r.NotNil(over)
	r.Equal(thread.UID, over.UID)

	stored, err = h.svc.ListMessages(ctx, thread.UID, 1000)
	r.NoError(err)
	r.Len(stored, support.DefaultMessagesPerThreadPerHour,
		"the dropped message must not have been stored")

	// PER THREAD, not global: a different sender is unaffected, so one flood
	// cannot silence everybody else's support conversations.
	_, otherMsg, err := h.svc.Capture(ctx, &support.Inbound{
		Channel: models.SupportChannelSMS, Identity: "+33612340001",
		ExternalID: "SM-other-thread", Body: "hello",
	})
	r.NoError(err)
	r.NotNil(otherMsg)

	// The window is HOURLY: an hour later the same thread is accepted again.
	h.now = h.now.Add(time.Hour + time.Minute)

	_, later, err := h.svc.Capture(ctx, &support.Inbound{
		Channel: models.SupportChannelSMS, Identity: identity,
		ExternalID: "SM-cap-later", Body: "still here",
		At: h.now,
	})
	r.NoError(err)
	r.NotNil(later, "the ceiling is per hour, not permanent")
}

// TestMirror_InstanceHourlyCeiling covers the instance-wide mirror cap, the
// sibling of the per-thread fold window.
//
// The fold window alone is not enough: a hundred DIFFERENT numbers texting once
// each would sail straight through it and produce a hundred emails, because
// each is its own thread. This is the ceiling that stops that.
func TestMirror_InstanceHourlyCeiling(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	h := newHarness(t, "support@acme.com")

	// One message each from many distinct identities, so the PER-THREAD fold
	// never applies and only the instance ceiling can bite.
	for i := range support.DefaultMirrorsPerHour {
		_, msg, err := h.svc.Capture(ctx, &support.Inbound{
			Channel: models.SupportChannelSMS, Identity: "+3361299" + strconv.Itoa(1000+i),
			ExternalID: "SM-flood-" + strconv.Itoa(i), Body: "hello",
		})
		r.NoError(err)
		r.NotNil(msg)
	}

	r.Equal(support.DefaultMirrorsPerHour, h.mailer.count(),
		"every distinct thread below the ceiling gets its own mirror")

	// Past the ceiling the mirror is suppressed.
	thread, msg, err := h.svc.Capture(ctx, &support.Inbound{
		Channel: models.SupportChannelSMS, Identity: "+33612997777",
		ExternalID: "SM-flood-over", Body: "anyone there?",
	})
	r.NoError(err)
	r.Equal(support.DefaultMirrorsPerHour, h.mailer.count(),
		"the instance hourly ceiling must suppress further mirrors")

	// THE MESSAGE IS STILL CAPTURED. Throttling a notification must never lose
	// the thing being notified about — that would put the black hole back.
	r.NotNil(msg)

	stored, err := h.svc.ListMessages(ctx, thread.UID, 0)
	r.NoError(err)
	r.Len(stored, 1)
	r.Equal("anyone there?", stored[0].Body)

	// The suppressed mirror is recorded as pending, so the next one that does
	// go out can say how many it stands for.
	suppressed, err := h.svc.GetThread(ctx, thread.UID)
	r.NoError(err)
	r.Equal(1, suppressed.PendingMirrors)

	// The window is HOURLY: an hour on, mirrors resume.
	h.now = h.now.Add(time.Hour + time.Minute)

	_, _, err = h.svc.Capture(ctx, &support.Inbound{
		Channel: models.SupportChannelSMS, Identity: "+33612998888",
		ExternalID: "SM-flood-after", Body: "hello again",
		At: h.now,
	})
	r.NoError(err)
	r.Equal(support.DefaultMirrorsPerHour+1, h.mailer.count(),
		"the ceiling is per hour, not permanent")
}
