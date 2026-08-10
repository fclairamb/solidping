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

	params := r.telegramAlertParams(ctx, jctx, log, incident)

	messageID, err := r.sendTelegramAlert(ctx, jctx, log, client, incident, contact.Value, params)
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
func (r *EscalationStepJobRun) telegramAlertParams(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, incident *models.Incident,
) *telegram.AlertParams {
	orgSlug := r.orgSlugFor(ctx, jctx, log, incident.OrganizationUID)

	return &telegram.AlertParams{
		State:       telegramStateLabel(incident),
		CheckName:   r.phoneCheckName(ctx, jctx, incident),
		Detail:      telegramDetail(incident),
		OrgSlug:     orgSlug,
		IncidentURL: telegramIncidentURL(appBaseURL(jctx), orgSlug, incident.UID),
	}
}

// sendTelegramAlert sends the alert, threading it under the incident's first
// message when there is one.
//
// Threading is a nicety and may NEVER cost a delivery: a missing or expired
// thread anchor sends standalone, and a reply id Telegram rejects (the user
// deleted the original) is retried immediately without it.
func (r *EscalationStepJobRun) sendTelegramAlert(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	client *telegram.Client, incident *models.Incident, chatID string,
	params *telegram.AlertParams,
) (int64, error) {
	body := telegram.BuildAlertHTML(params)
	replyTo := r.telegramThreadAnchor(ctx, jctx, log, incident, chatID)

	messageID, err := sendTelegramHonoringRetryAfter(ctx, log, client, &telegram.Message{
		ChatID:           chatID,
		HTML:             body,
		ReplyToMessageID: replyTo,
	})

	if err != nil && replyTo != 0 && errors.Is(err, telegram.ErrReplyTargetMissing) {
		log.InfoContext(ctx, "telegram thread anchor is gone; sending standalone",
			"incidentUID", incident.UID, "chatId", chatID)
		r.clearTelegramThreadAnchor(ctx, jctx, log, incident, chatID)

		messageID, err = sendTelegramHonoringRetryAfter(
			ctx, log, client, &telegram.Message{ChatID: chatID, HTML: body},
		)
	}

	if err != nil {
		return 0, err
	}

	if replyTo != 0 && params.State == telegram.StateResolved {
		// Best effort: rewrite the original so someone scrolling back does not
		// read a stale red alert as live. A failure here never costs anything —
		// the resolution message itself already went out above.
		if editErr := client.EditMessageText(
			ctx, chatID, replyTo, telegram.BuildResolvedOriginalHTML(params),
		); editErr != nil {
			log.InfoContext(ctx, "could not mark the original telegram alert resolved",
				"incidentUID", incident.UID, "chatId", chatID, "error", editErr)
		}
	}

	return messageID, nil
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
func (r *EscalationStepJobRun) telegramThreadAnchor(
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

	if entry == nil || entry.Value == nil {
		return 0
	}

	// JSONMap round-trips numbers as float64 through JSON.
	switch id := (*entry.Value)["messageId"].(type) {
	case float64:
		return int64(id)
	case int64:
		return id
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

	if existing := r.telegramThreadAnchor(ctx, jctx, log, incident, chatID); existing != 0 {
		return
	}

	ttl := telegramThreadTTL
	value := &models.JSONMap{"messageId": messageID}

	if err := jctx.DBService.SetStateEntry(ctx, &orgUID, key, value, &ttl); err != nil {
		// Threading is cosmetic; losing the anchor only means the next alert
		// is standalone.
		log.InfoContext(ctx, "could not store telegram thread anchor",
			"incidentUID", incident.UID, "error", err)
	}
}

// clearTelegramThreadAnchor drops an anchor Telegram no longer accepts, so the
// next alert does not pay the same rejected round-trip again.
func (r *EscalationStepJobRun) clearTelegramThreadAnchor(
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
