package support

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
)

// noticePreviewMax bounds how much of a customer's message travels in the
// notice. Enough to decide whether to drop everything and answer; not the
// whole conversation, which lives behind the link.
const noticePreviewMax = 200

// noticeFoldState is one thread's anti-burst state for operator notices.
//
// It is PARALLEL to the mail mirror's `support_threads.last_mirror_at` /
// `pending_mirrors` columns rather than shared with them, for two reasons: the
// mirror is disabled outright on an instance with no support mailbox (where
// operator notices must still work), and reusing its columns would make the
// mirror's own counters depend on whether anyone subscribed to notices — the
// mirror is the positive control in the tests, so it stays untouched.
//
// In process memory, like the mirror's instance-wide ceiling: a second replica
// folding independently costs at most one extra notice per window, which is
// the right trade against a schema change on a notification path.
type noticeFoldState struct {
	lastAt  time.Time
	pending int
}

// operatorNotices holds the fold state for every thread seen recently.
type operatorNotices struct {
	mu     sync.Mutex
	window time.Duration
	states map[string]*noticeFoldState
}

func newOperatorNotices(window time.Duration) *operatorNotices {
	return &operatorNotices{window: window, states: make(map[string]*noticeFoldState)}
}

// admit decides whether this thread may raise a notice right now.
//
// Returns (true, folded) when it may — folded being how many messages were
// swallowed since the last one, so the notice can say "…and N more". Returns
// (false, _) when the thread is still inside its fold window, having counted
// the message towards the next notice.
func (o *operatorNotices) admit(threadUID string, now time.Time) (bool, int) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.prune(now)

	state, ok := o.states[threadUID]
	if ok && now.Sub(state.lastAt) < o.window {
		state.pending++

		return false, 0
	}

	folded := 0
	if ok {
		folded = state.pending
	}

	o.states[threadUID] = &noticeFoldState{lastAt: now, pending: 0}

	return true, folded
}

// prune drops threads that have been quiet for two windows. Without it the map
// grows one entry per thread for the life of the process.
func (o *operatorNotices) prune(now time.Time) {
	cutoff := now.Add(-2 * o.window)
	for uid, state := range o.states {
		if state.lastAt.Before(cutoff) {
			delete(o.states, uid)
		}
	}
}

// raiseOperatorNotice tells the subscribed super admins that a human just
// wrote in.
//
// Additive to the mail mirror, never a replacement: an instance with a support
// mailbox keeps getting exactly the mail it got before. Never returns an error
// and never blocks — the message is already stored, and the webhook that
// delivered it is answering a provider under a deadline.
func (s *Service) raiseOperatorNotice(
	ctx context.Context, thread *models.SupportThread, msg *models.SupportMessage, isNewThread bool,
) {
	if msg == nil || thread == nil {
		return
	}

	now := s.now()

	admitted, folded := s.operatorNotices.admit(thread.UID, now)
	if !admitted {
		return
	}

	// Instance-wide ceiling, independent of the per-thread window: a hundred
	// distinct numbers texting once each must not produce a hundred pushes
	// either. Same posture, and the same number, as the mail mirror.
	if !s.noticesPerHour.allow("instance", now) {
		s.log.WarnContext(ctx, "operator notice hourly ceiling reached; notification suppressed",
			"ceiling", DefaultMirrorsPerHour)

		return
	}

	opsnotify.Notify(ctx, opsnotify.Notice{
		Event:   opsnotify.EventSupportMessage,
		Subject: supportNoticeSubject(thread, isNewThread),
		Body:    supportNoticeBody(thread, msg, folded),
		URL:     s.ThreadURL(thread.UID),
	})
}

// supportNoticeSubject is the one line a push title or an SMS lead gets.
func supportNoticeSubject(thread *models.SupportThread, isNewThread bool) string {
	verb := "New message"
	if isNewThread {
		verb = "New support thread"
	}

	return fmt.Sprintf("[SolidPing support] %s on %s from %s",
		verb, thread.Channel, thread.ChannelIdentity)
}

// supportNoticeBody renders the notice. Plain text only: the body is typed by
// a stranger, and every renderer downstream escapes it, so nothing here may
// look like markup.
func supportNoticeBody(thread *models.SupportThread, msg *models.SupportMessage, folded int) string {
	lines := []string{
		"Channel: " + thread.Channel,
		"From:    " + thread.ChannelIdentity,
		"",
		previewBody(msg.Body),
	}

	if folded > 0 {
		lines = append(lines, "",
			"…and "+strconv.Itoa(folded)+" more message(s) in this thread since the last notification.")
	}

	lines = append(lines, "",
		"Replying to this notification does NOT reach the sender — open the thread to answer them.")

	return strings.Join(lines, "\n")
}

// previewBody caps the quoted message. The full text is one link away.
func previewBody(body string) string {
	trimmed := strings.TrimSpace(body)
	if len([]rune(trimmed)) <= noticePreviewMax {
		return trimmed
	}

	return string([]rune(trimmed)[:noticePreviewMax]) + "…"
}
