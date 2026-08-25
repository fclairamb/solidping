package support

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// Mirror outcomes, used as the `outcome` metric label.
const (
	mirrorSent      = "sent"
	mirrorFolded    = "folded"
	mirrorThrottled = "throttled"
	mirrorFailed    = "failed"
	mirrorDisabled  = "disabled"
)

// mirrorNotice is the lead paragraph of every mirror. It is not decoration.
//
// An operator who receives an email about an inbound WhatsApp message will
// reply to that email, and the reply will go to whatever the mirror's own
// Reply-To says — never to the WhatsApp number. Left unsaid, this produces an
// operator who believes they answered a customer and a customer who heard
// nothing. So the mirror leads with what it is and with the only way to
// actually reply.
const mirrorNotice = "This is a notification, not a conversation. " +
	"Replying to this email does NOT reach the sender — open the thread to answer them."

// mirror sends the support-mailbox notification for a freshly captured message.
//
// Never returns an error: the message is already stored, and a bounced
// notification is a smaller problem than a lost message. Every outcome is
// counted, so a silent mirror outage is visible in
// solidping_support_mirror_total.
func (s *Service) mirror(
	ctx context.Context, thread *models.SupportThread, msg *models.SupportMessage, isNewThread bool,
) {
	if msg == nil || thread == nil {
		return
	}

	// No support mailbox configured: the feature stays off as a whole, by
	// design. Capture still happened.
	if s.replyTo == "" || s.mailer == nil {
		prommetrics.SupportMirror.WithLabelValues(mirrorDisabled).Inc()

		return
	}

	now := s.now()

	// Per-thread fold window. A burst collapses into one mail carrying the
	// count; the messages themselves are all in the thread regardless.
	if thread.LastMirrorAt != nil && now.Sub(*thread.LastMirrorAt) < s.mirrorFoldWindow {
		prommetrics.SupportMirror.WithLabelValues(mirrorFolded).Inc()
		s.bumpPendingMirrors(ctx, thread, now)

		return
	}

	// Instance-wide ceiling, independent of the per-thread window: a hundred
	// distinct numbers texting once each must not produce a hundred emails
	// either.
	if !s.mirrorsPerHour.allow("instance", now) {
		prommetrics.SupportMirror.WithLabelValues(mirrorThrottled).Inc()
		s.bumpPendingMirrors(ctx, thread, now)
		s.log.WarnContext(ctx, "support mirror hourly ceiling reached; notification suppressed",
			"ceiling", DefaultMirrorsPerHour)

		return
	}

	folded := thread.PendingMirrors
	mail := s.buildMirror(thread, msg, isNewThread, folded)

	if _, err := s.mailer.Send(ctx, mail); err != nil {
		prommetrics.SupportMirror.WithLabelValues(mirrorFailed).Inc()
		s.log.WarnContext(ctx, "failed to mirror support message to the support mailbox",
			"threadUid", thread.UID, "channel", thread.Channel, "error", err)

		return
	}

	prommetrics.SupportMirror.WithLabelValues(mirrorSent).Inc()
	s.clearPendingMirrors(ctx, thread, now)
}

// buildMirror renders the notification email.
func (s *Service) buildMirror(
	thread *models.SupportThread, msg *models.SupportMessage, isNewThread bool, folded int,
) *email.Message {
	link := s.ThreadURL(thread.UID)

	verb := "New message"
	if isNewThread {
		verb = "New support thread"
	}

	subject := fmt.Sprintf("[SolidPing support] %s on %s from %s",
		verb, thread.Channel, thread.ChannelIdentity)

	var extra string
	if folded > 0 {
		extra = fmt.Sprintf("%d earlier message(s) from this thread were folded into this notification.", folded)
	}

	text := strings.Join([]string{
		mirrorNotice,
		"",
		"Channel: " + thread.Channel,
		"From:    " + thread.ChannelIdentity,
		"Thread:  " + link,
		"",
		msg.Body,
		"",
		extra,
	}, "\n")

	// Bodies are attacker-influenced. They are ESCAPED, never interpolated as
	// markup — a support notification must not be a vector for sending our own
	// operators a crafted HTML email.
	htmlBody := "<html><body>" +
		"<p><strong>" + html.EscapeString(mirrorNotice) + "</strong></p>" +
		"<p>Channel: " + html.EscapeString(thread.Channel) + "<br>" +
		"From: " + html.EscapeString(thread.ChannelIdentity) + "</p>" +
		"<p><a href=\"" + html.EscapeString(link) + "\">Open the thread to reply</a></p>" +
		"<hr>" +
		"<pre style=\"white-space:pre-wrap;font-family:inherit\">" +
		html.EscapeString(msg.Body) + "</pre>"

	if extra != "" {
		htmlBody += "<p><em>" + html.EscapeString(extra) + "</em></p>"
	}

	htmlBody += "</body></html>"

	return &email.Message{
		Recipients: email.Recipients{To: []string{s.replyTo}},
		Subject:    subject,
		Text:       text,
		HTML:       htmlBody,
		// RFC 3834 plus our private marker. Written now, while nothing reads
		// them, precisely so the later inbound-email capture can skip our own
		// mirrors from day one instead of shipping a mail loop first.
		AutoSubmitted: true,
		SupportMirror: true,
		// Explicitly NOT support-replyable: this mail already goes TO the
		// support mailbox, so pointing its Reply-To back at the same address
		// would make it reply to itself.
		SupportReplyable: false,
	}
}

// ThreadURL is the deep link an operator follows to answer.
func (s *Service) ThreadURL(threadUID string) string {
	return s.baseURL + "/dash0/support/" + threadUID
}

func (s *Service) bumpPendingMirrors(ctx context.Context, thread *models.SupportThread, now time.Time) {
	thread.PendingMirrors++

	if _, err := s.bun().NewUpdate().Model((*models.SupportThread)(nil)).
		Set("pending_mirrors = pending_mirrors + 1").
		Set("updated_at = ?", now).
		Where("uid = ?", thread.UID).
		Exec(ctx); err != nil {
		s.log.WarnContext(ctx, "failed to record folded support mirror",
			"threadUid", thread.UID, "error", err)
	}
}

func (s *Service) clearPendingMirrors(ctx context.Context, thread *models.SupportThread, now time.Time) {
	thread.PendingMirrors = 0
	thread.LastMirrorAt = &now

	if _, err := s.bun().NewUpdate().Model((*models.SupportThread)(nil)).
		Set("pending_mirrors = 0").
		Set("last_mirror_at = ?", now).
		Set("updated_at = ?", now).
		Where("uid = ?", thread.UID).
		Exec(ctx); err != nil {
		s.log.WarnContext(ctx, "failed to record support mirror send",
			"threadUid", thread.UID, "error", err)
	}
}
