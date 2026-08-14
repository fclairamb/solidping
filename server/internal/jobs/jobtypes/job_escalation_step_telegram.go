package jobtypes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

const (
	// telegramDetailCap bounds the free-text detail line. Telegram's own limit
	// is 4096 characters for the whole message; this keeps a single alert
	// glanceable on a phone lock screen.
	telegramDetailCap = 200
	// telegramThreadStatePrefix namespaces the per-incident, per-chat message
	// id used for threading.
	telegramThreadStatePrefix = "telegram_msg:"
	// telegramThreadTTL is how long a thread anchor survives. Well past any
	// incident lifetime, but bounded so the state table cannot grow without
	// limit.
	telegramThreadTTL = 7 * 24 * time.Hour
	// telegramThreadMessageIDField is the key holding the anchor message id
	// inside a thread-anchor state entry.
	telegramThreadMessageIDField = "messageId"
)

// pageTelegram delivers an escalation alert to a user's connected Telegram chat
// through the instance-level bot. It mirrors pageWhatsApp:
//
//   - an unverified contact is never messaged (pressing Start IS the opt-in, so
//     an unverified telegram contact means the link was revoked or the bot was
//     blocked);
//   - an instance with Telegram off degrades to an info-log skip;
//   - the per-org hourly runaway guard is reserved before the send — but there
//     is NO monthly quota, because Telegram messages are free and metering a
//     free channel would be metering for its own sake;
//   - a send failure returns 0 so the escalation step falls through to the next
//     step, precisely as an SMS or WhatsApp failure does.
//
// Two things are specific to this channel. On a permanent per-contact failure
// (blocked, chat gone) the contact's VerifiedAt is cleared: a blocked contact
// that stayed "verified" would be a route silently swallowing every future
// page, which is the worst failure mode a paging channel has. And threading is
// strictly best-effort — every threading failure degrades to a plain standalone
// message rather than costing the delivery.
func (r *EscalationStepJobRun) pageTelegram(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, route *models.UserNotificationRoute, filter map[string]bool,
) int {
	if !severityAllowsTelegram(filter) {
		return 0
	}

	contact := route.Contact

	if contact.VerifiedAt == nil {
		log.InfoContext(ctx, "telegram contact not verified; skipping route",
			"contactUID", contact.UID, "userUID", route.UserUID)

		return 0
	}

	// Configured(), not Active(): sending an alert only needs the token. The
	// bot @username is required to BUILD a connect link, never to use a chat
	// that is already connected and verified.
	if jctx.AppConfig == nil || !jctx.AppConfig.Telegram.Configured() {
		log.InfoContext(ctx, "telegram not configured on this instance; skipping route",
			"contactUID", contact.UID, "orgUID", incident.OrganizationUID)

		return 0
	}

	client, err := telegram.NewClientFromConfig(&jctx.AppConfig.Telegram)
	if err != nil {
		log.InfoContext(ctx, "telegram client unavailable; skipping route",
			"contactUID", contact.UID, "error", err)

		return 0
	}

	if !r.reserveTelegram(ctx, jctx, log, incident, contact.Value) {
		return 0
	}

	params := telegramAlertParams(ctx, jctx, log, incident)

	messageID, err := sendTelegramAlert(ctx, jctx, log, client, incident, contact.Value, params)
	if err != nil {
		reason := telegram.FailureReason(err)

		log.WarnContext(ctx, "failed to send escalation Telegram message",
			"contactUID", contact.UID, "reason", reason, "error", err)
		r.auditPhoneSkip(ctx, jctx, log, incident, reason)
		r.disableTelegramContactIfDead(ctx, jctx, log, contact, err)

		return 0
	}

	r.recordTelegramThread(ctx, jctx, log, incident, contact.Value, messageID, params)
	r.auditPhoneSend(ctx, jctx, log, incident, route.UserUID,
		models.UserContactTypeTelegram, strconv.FormatInt(messageID, 10))

	return 1
}

// reserveTelegram applies the per-run dedup and the hourly runaway guard.
// Deliberately NOT reservePhoneChannel: that path also reserves a monthly
// org_usage_counter, and Telegram has no monthly quota to reserve.
func (r *EscalationStepJobRun) reserveTelegram(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, chatID string,
) bool {
	key := models.UserContactTypeTelegram + ":" + chatID
	if r.sentPhones[key] {
		// Same chat already handled in this job run (e.g. the user matched via
		// both `user` and `all_admins`).
		return false
	}

	if jctx.Services != nil && jctx.Services.Entitlements != nil {
		if err := jctx.Services.Entitlements.ReserveTelegram(ctx, incident.OrganizationUID); err != nil {
			log.InfoContext(ctx, "telegram runaway guard reached; skipping send",
				"orgUID", incident.OrganizationUID, "error", err)
			r.auditPhoneSkip(ctx, jctx, log, incident,
				entitlements.RunawayKindTelegram+"_runaway_guard")

			return false
		}
	}

	if r.sentPhones == nil {
		r.sentPhones = make(map[string]bool)
	}

	r.sentPhones[key] = true

	return true
}

// telegramAlertParams assembles the RAW (unescaped) alert values. Escaping
// happens exactly once, inside telegram.BuildAlertHTML — pre-escaping here
// would double-escape and render "&amp;amp;".
//
// A plain function rather than a method on the escalation run: nothing here
// reads run state, and the resolution-notice job needs the very same values, so
// both callers share ONE definition of what a Telegram message says.
func telegramAlertParams(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, incident *models.Incident,
) *telegram.AlertParams {
	orgSlug := orgSlugForOrg(ctx, jctx, log, incident.OrganizationUID)

	return &telegram.AlertParams{
		State:       telegramStateLabel(incident),
		Number:      incident.Number,
		IncidentUID: incident.UID,
		CheckName:   incidentCheckName(ctx, jctx, incident),
		Detail:      telegramDetail(incident),
		OrgSlug:     orgSlug,
		IncidentURL: telegramIncidentURL(appBaseURL(jctx), orgSlug, incident.UID),
	}
}

// telegramAckKeyboard attaches the Acknowledge button to an alert — but only
// while there is something to acknowledge. A resolved or already-acked incident
// ships without one: a button that answers "already done" is noise, and worse,
// an alert still offering it reads as an unclaimed page.
func telegramAckKeyboard(incident *models.Incident) *telegram.InlineKeyboard {
	if incident.State == models.IncidentStateResolved || incident.AcknowledgedAt != nil {
		return nil
	}

	return telegram.AckKeyboard(incident.UID)
}

// sendTelegramAlert sends the alert, threading it under the incident's first
// message when there is one.
//
// Threading is a nicety and may NEVER cost a delivery: a missing or expired
// thread anchor sends standalone, and a reply id Telegram rejects (the user
// deleted the original) is retried immediately without it.
//
// Threading, that degradation, the resolution edit and the retry_after handling
// live here exactly once: the escalation step and the resolution-notice job both
// go through this function, because duplicating any of it is how the two paths
// would drift into disagreeing about what a resolved incident looks like.
func sendTelegramAlert(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	client *telegram.Client, incident *models.Incident, chatID string,
	params *telegram.AlertParams,
) (int64, error) {
	return sendTelegramAlertTo(
		ctx, jctx, log, client, incident, chatID, params,
		telegramThreadAnchor(ctx, jctx, log, incident, chatID),
	)
}

// sendTelegramAlertTo is sendTelegramAlert with an EXPLICIT reply target
// (0 = standalone).
//
// It exists because the resolution-notice job claims a chat by deleting its
// anchor before sending — by then the stored anchor is gone, but the message id
// it held is still exactly what the notice must thread under and rewrite.
func sendTelegramAlertTo(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	client *telegram.Client, incident *models.Incident, chatID string,
	params *telegram.AlertParams, replyTo int64,
) (int64, error) {
	// Decide reference qualification HERE, per destination chat, because this is
	// the single funnel both the escalation step and the resolution-notice job
	// go through and the only place that knows which chat the message is for.
	// The resolution job builds ONE params for every chat it notifies, so the
	// copy is load-bearing: mutating the caller's struct would leak one chat's
	// decision into the next one's message.
	perChat := *params
	perChat.QualifyRef = telegramChatQualifiesRefs(ctx, jctx, log, chatID)
	params = &perChat

	body := telegram.BuildAlertHTML(params)
	keyboard := telegramAckKeyboard(incident)

	messageID, err := sendTelegramHonoringRetryAfter(ctx, log, client, &telegram.Message{
		ChatID:           chatID,
		HTML:             body,
		ReplyToMessageID: replyTo,
		ReplyMarkup:      keyboard,
	})

	if err != nil && replyTo != 0 && errors.Is(err, telegram.ErrReplyTargetMissing) {
		log.InfoContext(ctx, "telegram thread anchor is gone; sending standalone",
			"incidentUID", incident.UID, "chatId", chatID)
		clearTelegramThreadAnchor(ctx, jctx, log, incident, chatID)

		messageID, err = sendTelegramHonoringRetryAfter(
			ctx, log, client, &telegram.Message{ChatID: chatID, HTML: body, ReplyMarkup: keyboard},
		)
	}

	if err != nil {
		return 0, err
	}

	if replyTo != 0 && params.State == telegram.StateResolved {
		// Best effort: rewrite the original so someone scrolling back does not
		// read a stale red alert as live, and TAKE ITS ACKNOWLEDGE BUTTON AWAY —
		// a resolved incident has nothing left to ack. An absent reply_markup
		// would leave the old keyboard in place, so the empty one is explicit.
		if editErr := client.EditMessage(ctx, &telegram.Edit{
			ChatID:      chatID,
			MessageID:   replyTo,
			HTML:        telegram.BuildResolvedOriginalHTML(params),
			ReplyMarkup: telegram.EmptyInlineKeyboard(),
		}); editErr != nil {
			log.InfoContext(ctx, "could not mark the original telegram alert resolved",
				"incidentUID", incident.UID, "chatId", chatID, "error", editErr)
		}
	}

	return messageID, nil
}

// telegramChatQualifiesRefs reports whether messages to this chat must carry
// ORG-QUALIFIED incident references ("#acme:42" rather than "#42").
//
// True exactly when the chat is verified-linked in two or more organizations.
// Incident numbers are per-org sequential, so "#42" exists in every org: in such
// a chat the short reference is genuinely ambiguous, both for the human reading
// the alert and for the bot parsing the `/ack 42` they type back. A single-org
// chat — the overwhelming majority — keeps the short form and notices nothing.
//
// A lookup failure degrades to the short form: an unqualified reference is the
// pre-existing behavior, whereas failing the send would cost a page.
func telegramChatQualifiesRefs(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, chatID string,
) bool {
	contacts, err := jctx.DBService.ListUserContactsByTypeValue(
		ctx, models.UserContactTypeTelegram, chatID,
	)
	if err != nil {
		log.InfoContext(ctx, "could not count the orgs linked to a telegram chat; sending a short ref",
			"chatId", chatID, "error", err)

		return false
	}

	orgs := make(map[string]bool, len(contacts))

	for _, contact := range contacts {
		// Unverified rows are revoked or blocked links: they receive nothing, so
		// they must not make an otherwise single-org chat read as multi-org.
		if contact.VerifiedAt == nil {
			continue
		}

		orgs[contact.OrganizationUID] = true
	}

	return len(orgs) > 1
}

// telegramMaxRetryAfter bounds how long a single send will wait out a Telegram
// throttle inline. Telegram's own retry_after for a per-chat burst is usually a
// second or two; anything longer belongs to the next escalation repeat, not to
// a job holding a worker slot.
const telegramMaxRetryAfter = 5 * time.Second

// sendTelegramHonoringRetryAfter sends once and, on a throttle, waits exactly
// as long as TELEGRAM asked before retrying once.
//
// This is the point of parsing `parameters.retry_after` at all: a generic
// backoff either gives up too early (the page is lost to a one-second burst
// limit) or waits far too long. Telegram's own number is the only one that
// clears the throttle without compounding it.
func sendTelegramHonoringRetryAfter(
	ctx context.Context, log *slog.Logger, client *telegram.Client, msg *telegram.Message,
) (int64, error) {
	messageID, err := client.SendMessage(ctx, msg)
	if err == nil || !errors.Is(err, telegram.ErrRateLimited) {
		return messageID, err
	}

	wait := telegram.RetryAfter(err)
	if wait <= 0 || wait > telegramMaxRetryAfter {
		// No hint, or a cooldown too long to hold a worker for: let the step
		// fall through and let the next escalation repeat try again.
		return 0, err
	}

	log.InfoContext(ctx, "telegram throttled the send; waiting the requested cooldown",
		"retryAfter", wait)

	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return 0, err
	case <-timer.C:
	}

	return client.SendMessage(ctx, msg)
}

// telegramThreadKey is the state key holding an incident's first message id in
// one chat.
func telegramThreadKey(incidentUID, chatID string) string {
	return telegramThreadStatePrefix + incidentUID + ":" + chatID
}

// telegramThreadAnchor returns the message id to reply to, or 0 when this is
// the first alert for the incident in this chat (or the anchor expired).
func telegramThreadAnchor(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, chatID string,
) int64 {
	orgUID := incident.OrganizationUID

	entry, err := jctx.DBService.GetStateEntry(ctx, &orgUID, telegramThreadKey(incident.UID, chatID))
	if err != nil {
		// A lookup failure must not stop the page — send standalone.
		log.InfoContext(ctx, "could not read telegram thread anchor; sending standalone",
			"incidentUID", incident.UID, "error", err)

		return 0
	}

	return telegramAnchorMessageID(entry)
}

// telegramAnchorMessageID reads the stored message id out of a thread-anchor
// entry. Shared with the resolution-notice job, which gets its entries from a
// prefix listing rather than a single lookup.
func telegramAnchorMessageID(entry *models.StateEntry) int64 {
	if entry == nil || entry.Value == nil {
		return 0
	}

	// JSONMap round-trips numbers as float64 through JSON.
	switch id := (*entry.Value)[telegramThreadMessageIDField].(type) {
	case float64:
		return int64(id)
	case int64:
		return id
	case int:
		return int64(id)
	default:
		return 0
	}
}

// recordTelegramThread stores the first message id for an incident+chat so
// later alerts thread under it. Never stored twice: the anchor must stay the
// FIRST message, otherwise the thread walks forward one reply at a time.
func (r *EscalationStepJobRun) recordTelegramThread(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, chatID string, messageID int64, params *telegram.AlertParams,
) {
	if messageID == 0 || params.State == telegram.StateResolved {
		// A resolution never becomes an anchor: the incident is over.
		return
	}

	orgUID := incident.OrganizationUID
	key := telegramThreadKey(incident.UID, chatID)

	if existing := telegramThreadAnchor(ctx, jctx, log, incident, chatID); existing != 0 {
		return
	}

	ttl := telegramThreadTTL
	value := &models.JSONMap{telegramThreadMessageIDField: messageID}

	if err := jctx.DBService.SetStateEntry(ctx, &orgUID, key, value, &ttl); err != nil {
		// Threading is cosmetic; losing the anchor only means the next alert
		// is standalone.
		log.InfoContext(ctx, "could not store telegram thread anchor",
			"incidentUID", incident.UID, "error", err)
	}
}

// clearTelegramThreadAnchor drops an anchor Telegram no longer accepts, so the
// next alert does not pay the same rejected round-trip again.
func clearTelegramThreadAnchor(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, chatID string,
) {
	orgUID := incident.OrganizationUID
	if _, err := jctx.DBService.DeleteStateEntry(
		ctx, &orgUID, telegramThreadKey(incident.UID, chatID),
	); err != nil {
		log.InfoContext(ctx, "could not clear stale telegram thread anchor",
			"incidentUID", incident.UID, "error", err)
	}
}

// disableTelegramContactIfDead un-verifies a contact the bot can no longer
// reach. The dashboard then renders it as "Reconnect needed" instead of
// pretending it is a live paging route.
func (r *EscalationStepJobRun) disableTelegramContactIfDead(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	contact *models.UserContact, sendErr error,
) {
	if !telegram.ContactDisabling(sendErr) {
		return
	}

	if err := jctx.DBService.ClearUserContactVerified(ctx, contact.UID); err != nil {
		log.WarnContext(ctx, "failed to un-verify unreachable telegram contact",
			"contactUID", contact.UID, "error", err)

		return
	}

	log.InfoContext(ctx, "telegram contact is unreachable; marked as needing reconnection",
		"contactUID", contact.UID, "reason", telegram.FailureReason(sendErr))
}

// telegramStateLabel renders the incident's current state. One message shape
// covers down / escalated / resolved precisely because this is a variable.
func telegramStateLabel(incident *models.Incident) string {
	switch {
	case incident.State == models.IncidentStateResolved:
		return telegram.StateResolved
	case incident.EscalatedAt != nil:
		return telegram.StateEscalated
	default:
		return telegram.StateDown
	}
}

// telegramDetail builds the detail line: the incident title when it has one,
// otherwise how long it has been open. Capped and single-line — it is an alert
// body, not a log.
func telegramDetail(incident *models.Incident) string {
	detail := ""
	if incident.Title != nil {
		detail = strings.TrimSpace(*incident.Title)
	}

	if detail == "" {
		since := time.Since(incident.StartedAt).Round(time.Minute)
		detail = fmt.Sprintf("open for %s", since)
	}

	detail = strings.Join(strings.Fields(detail), " ")

	if len(detail) > telegramDetailCap {
		detail = detail[:telegramDetailCap-1] + "…"
	}

	return detail
}

// telegramIncidentURL builds the dashboard deep link, or "" when the instance
// has no base URL configured (the alert then simply ships without a link).
func telegramIncidentURL(baseURL, orgSlug, incidentUID string) string {
	if baseURL == "" || orgSlug == "" {
		return ""
	}

	return strings.TrimRight(baseURL, "/") + "/dash0/orgs/" + orgSlug + "/incidents/" + incidentUID
}
