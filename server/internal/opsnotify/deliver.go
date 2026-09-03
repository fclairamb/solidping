package opsnotify

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
	"github.com/fclairamb/solidping/server/internal/integrations/twilio"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// smsMaxLen bounds the SMS rendering of a notice. A notice is a short report;
// an SMS is not. The subject line carries the headline and the operator opens
// the full one on another route.
const smsMaxLen = 300

// Delivery outcomes, used as the `outcome` metric label.
const (
	// outcomeSent means the medium accepted the notice.
	outcomeSent = "sent"
	// outcomeFailed means the medium was available and refused it.
	outcomeFailed = "failed"
	// outcomeSkipped means this instance cannot carry that contact type at
	// all — an unsupported type, or a medium that is not configured.
	outcomeSkipped = "skipped"
)

// contactTypeNone is the `contact_type` label for outcomes that happen before
// any route is picked (no recipient, no route, dispatcher failure).
const contactTypeNone = "none"

// ErrMediumUnavailable marks "this instance cannot carry that contact type" —
// no Telegram bot token, no web-push keys, no SMS provider for the org, no
// Slack connection — as opposed to "the provider was there and refused it".
//
// A medium closure returns it (wrapped) so delivery can tell the two apart.
// The distinction is the entire reason `solidping_operator_notice_total` has an
// `outcome` label: an instance that never configured Telegram must not look
// like an instance whose Telegram is broken, or the metric that exists to keep
// silent drops visible becomes the thing hiding them.
var ErrMediumUnavailable = errors.New("this instance cannot deliver over that medium")

// Human labels for the notice kinds that travel this transport.
const (
	labelWatchdogDigest = "Platform watchdog digest"
	labelOperatorNotice = "Operator notice"
)

// noticeLabel names the notice in a log line.
//
// The watchdog keeps its own wording: this transport was extracted FROM the
// watchdog, its digests still flow through it, and an operator grepping their
// logs (or an alert rule matching them) should not have the watchdog silently
// start reporting itself under a name that never mentions the watchdog.
func noticeLabel(event string) string {
	if event == EventWatchdogDigest {
		return labelWatchdogDigest
	}

	return labelOperatorNotice
}

// skipUnavailable records a route this instance simply cannot carry.
func skipUnavailable(
	ctx context.Context, log *slog.Logger, label, medium string, route *models.UserNotificationRoute,
) string {
	log.WarnContext(ctx,
		label+" cannot be delivered: "+medium+" is not available on this instance; skipping route",
		"contactUid", route.Contact.UID)

	return outcomeSkipped
}

// classifySend turns one medium's result into an outcome label, keeping
// "not configured" (skipped) distinct from "refused it" (failed).
func classifySend(
	ctx context.Context, log *slog.Logger, label, medium string,
	route *models.UserNotificationRoute, err error,
) string {
	if err == nil {
		return outcomeSent
	}

	if errors.Is(err, ErrMediumUnavailable) {
		log.WarnContext(ctx,
			label+" cannot be delivered: "+medium+" is not available on this instance; skipping route",
			"contactUid", route.Contact.UID, "error", err)

		return outcomeSkipped
	}

	log.WarnContext(ctx, label+" failed to send over "+medium,
		"orgUid", route.OrgUID, "contactUid", route.Contact.UID, "error", err)

	return outcomeFailed
}

// SendFunc is one medium. Every one of them is nil-able: a nil closure means
// "this instance cannot carry that contact type", which is WARNed and counted
// as skipped rather than silently dropped.
type (
	// EnqueueEmailFunc hands the notice to the normal email job chain, so it
	// inherits the retry policy and SMTP configuration every other mail uses.
	EnqueueEmailFunc func(ctx context.Context, orgUID, to, subject, text string) error
	// SendTelegramFunc DMs pre-rendered (already escaped) HTML to a chat id.
	SendTelegramFunc func(ctx context.Context, chatID, html string) error
	// SendSlackDMFunc DMs plain text through the org's own Slack connection.
	SendSlackDMFunc func(ctx context.Context, orgUID, slackUserID, text string) error
	// SendWebPushFunc pushes a title, body and click-through URL to a stored
	// subscription.
	SendWebPushFunc func(ctx context.Context, subscription, title, body, url string) error
	// SendSMSFunc texts a VERIFIED number through the org's resolved sender.
	SendSMSFunc func(ctx context.Context, orgUID, to, body string) error
)

// Deps is everything the transport needs from the rest of the process.
//
// The media are closures rather than concrete clients for the import-cycle
// reason documented on the package: `integrations/slack` imports
// `handlers/auth`, which raises notices. `internal/opsnotifywire` builds them.
type Deps struct {
	// DB reads the recipient, their memberships and their routes.
	DB db.Service

	EnqueueEmail EnqueueEmailFunc
	SendTelegram SendTelegramFunc
	SendSlackDM  SendSlackDMFunc
	SendWebPush  SendWebPushFunc
	SendSMS      SendSMSFunc
}

// DeliveryReport is what one recipient's fan-out did. It is returned rather
// than an error on purpose: a notice is best-effort per medium, and a single
// dead route must never abort the others or fail the caller.
type DeliveryReport struct {
	// UserUID is the recipient this report is about.
	UserUID string
	// Routes is how many enabled, deduplicated routes were considered.
	Routes int
	// Delivered is how many media accepted the notice.
	Delivered int
	// Failed is how many media were tried and refused it.
	Failed int
	// Skipped is how many routes this instance cannot carry at all.
	Skipped int
}

// Undeliverable reports that nothing at all reached this recipient — the state
// that must never be silent.
func (r DeliveryReport) Undeliverable() bool {
	return r.Delivered == 0
}

// DeliverToUser fans one notice out over a recipient's enabled routes, in
// position order, deduplicated by destination across the user's orgs.
//
// It never returns an error. Every skipped or failed route is logged and
// metered, because a silent drop on a notification path is the exact bug this
// transport exists to kill.
func DeliverToUser(
	ctx context.Context, deps Deps, log *slog.Logger, userUID string, notice *Notice,
) DeliveryReport {
	log = log.With("recipientUid", userUID, "event", notice.Event)
	label := noticeLabel(notice.Event)
	report := DeliveryReport{UserUID: userUID}

	if deps.DB == nil {
		log.WarnContext(ctx, label+" undeliverable: no database service wired")
		count(notice.Event, contactTypeNone, outcomeSkipped)

		return report
	}

	user, err := deps.DB.GetUser(ctx, userUID)
	if err != nil || user == nil {
		log.WarnContext(ctx, label+" undeliverable: recipient user not found", "error", err)
		count(notice.Event, contactTypeNone, outcomeSkipped)

		return report
	}

	routes := routesFor(ctx, deps, log, userUID)
	report.Routes = len(routes)

	for _, route := range routes {
		switch dispatchRoute(ctx, deps, log, label, route, notice) {
		case outcomeSent:
			report.Delivered++
		case outcomeFailed:
			report.Failed++
		default:
			report.Skipped++
		}
	}

	if report.Delivered == 0 {
		// "undeliverable" is asserted by the watchdog's own tests: a recipient
		// nobody could reach must be NAMED, not counted.
		log.WarnContext(ctx,
			label+" undeliverable: recipient has no enabled notification route that can carry it",
			"email", user.Email, "routes", report.Routes)
		count(notice.Event, contactTypeNone, outcomeSkipped)

		return report
	}

	log.InfoContext(ctx, label+" delivered", "deliveries", report.Delivered)

	return report
}

// routesFor collects a recipient's enabled routes across every organization
// they belong to.
//
// Notification routes are org-scoped (a Slack DM needs the org's bot token),
// but an instance-level notice has no org of its own. Walking the recipient's
// memberships is what lets an operator be reached through the contacts they
// already configured, wherever they configured them.
//
// Destinations are de-duplicated by contact TYPE + VALUE, never by contact
// UID: `user_contacts` rows are org-scoped, so the same email address
// registered in two orgs is two distinct UIDs belonging to the same human. A
// UID-keyed dedup therefore never fires across orgs and mails that person the
// notice twice. The first route wins, so the caller still sees the routes in
// `position` order.
func routesFor(
	ctx context.Context, deps Deps, log *slog.Logger, userUID string,
) []*models.UserNotificationRoute {
	members, err := deps.DB.ListMembersByUser(ctx, userUID)
	if err != nil {
		log.WarnContext(ctx, "Could not list the notice recipient's organizations", "error", err)

		return nil
	}

	seen := make(map[string]bool)
	out := make([]*models.UserNotificationRoute, 0, len(members))

	for _, member := range members {
		routes, routesErr := deps.DB.ListUserContactsWithRoutes(ctx, userUID, member.OrganizationUID)
		if routesErr != nil {
			log.WarnContext(ctx, "Could not list the notice recipient's notification routes",
				"orgUid", member.OrganizationUID, "error", routesErr)

			continue
		}

		for _, route := range routes {
			if !route.Enabled || route.Contact == nil {
				continue
			}

			key := destinationKey(route.Contact)
			if seen[key] {
				continue
			}

			seen[key] = true

			out = append(out, route)
		}
	}

	return out
}

// destinationKey is the identity of a delivery DESTINATION rather than of a
// database row: the contact's type plus its normalized value.
//
// Normalization is deliberately conservative — trim everywhere, and lowercase
// only for email, whose local part is case-insensitive in every mail system
// anyone actually runs. A Slack user id, a Telegram chat id and a web-push
// subscription are all case-sensitive opaque tokens, so lowercasing them would
// merge two genuinely different destinations.
func destinationKey(contact *models.UserContact) string {
	value := strings.TrimSpace(contact.Value)
	if contact.Type == models.UserContactTypeEmail {
		value = strings.ToLower(value)
	}

	// The NUL separator cannot occur in either half, so no type/value pair can
	// collide with another by concatenation.
	return contact.Type + "\x00" + value
}

// dispatchRoute delivers the notice over one route and returns the outcome
// label it was counted under.
func dispatchRoute(
	ctx context.Context, deps Deps, log *slog.Logger, label string,
	route *models.UserNotificationRoute, notice *Notice,
) string {
	var outcome string

	switch route.Contact.Type {
	case models.UserContactTypeEmail:
		outcome = sendEmail(ctx, deps, log, label, route, notice)
	case models.UserContactTypeTelegram:
		outcome = sendTelegram(ctx, deps, log, label, route, notice)
	case models.UserContactTypeSlackUser:
		outcome = sendSlackDM(ctx, deps, log, label, route, notice)
	case models.UserContactTypeWebPush:
		outcome = sendWebPush(ctx, deps, log, label, route, notice)
	case models.UserContactTypePhone:
		outcome = sendSMS(ctx, deps, log, label, route, notice)
	default:
		// WhatsApp (template-gated: Meta will not carry free-form business
		// text outside a session), pushover, ntfy, and anything added later.
		// Named in the log rather than dropped, so an operator whose only
		// route is one of these finds out from the logs instead of from a
		// missed message.
		log.WarnContext(ctx, label+" cannot be delivered over this contact type; skipping route",
			"contactType", route.Contact.Type, "contactUid", route.Contact.UID)

		outcome = outcomeSkipped
	}

	count(notice.Event, route.Contact.Type, outcome)

	return outcome
}

// sendEmail enqueues the notice as a normal email job.
func sendEmail(
	ctx context.Context, deps Deps, log *slog.Logger, label string,
	route *models.UserNotificationRoute, notice *Notice,
) string {
	if deps.EnqueueEmail == nil {
		return skipUnavailable(ctx, log, label, models.UserContactTypeEmail, route)
	}

	return classifySend(ctx, log, label, models.UserContactTypeEmail, route,
		deps.EnqueueEmail(ctx, route.OrgUID, route.Contact.Value, notice.Subject, notice.noticeText()))
}

// sendTelegram DMs the notice through the instance bot.
func sendTelegram(
	ctx context.Context, deps Deps, log *slog.Logger, label string,
	route *models.UserNotificationRoute, notice *Notice,
) string {
	if deps.SendTelegram == nil {
		return skipUnavailable(ctx, log, label, models.UserContactTypeTelegram, route)
	}

	// Bodies are attacker-influenced (a support message is typed by a
	// stranger), so every half goes through the escaper. Nothing here is ever
	// interpolated as markup.
	html := "<b>" + telegram.EscapeHTML(notice.Subject) + "</b>\n<pre>" +
		telegram.EscapeHTML(notice.Body) + "</pre>"
	if notice.URL != "" {
		html += "\n<a href=\"" + telegram.EscapeHTML(notice.URL) + "\">" +
			telegram.EscapeHTML(notice.URL) + "</a>"
	}

	return classifySend(ctx, log, label, models.UserContactTypeTelegram, route,
		deps.SendTelegram(ctx, route.Contact.Value, html))
}

// sendSlackDM delivers the notice as a Slack DM through the org's own Slack
// connection — the same path escalation DMs take.
func sendSlackDM(
	ctx context.Context, deps Deps, log *slog.Logger, label string,
	route *models.UserNotificationRoute, notice *Notice,
) string {
	if deps.SendSlackDM == nil {
		return skipUnavailable(ctx, log, label, models.UserContactTypeSlackUser, route)
	}

	return classifySend(ctx, log, label, models.UserContactTypeSlackUser, route,
		deps.SendSlackDM(ctx, route.OrgUID, route.Contact.Value, notice.noticeText()))
}

// sendWebPush pushes the subject line plus the first content line.
func sendWebPush(
	ctx context.Context, deps Deps, log *slog.Logger, label string,
	route *models.UserNotificationRoute, notice *Notice,
) string {
	if deps.SendWebPush == nil {
		return skipUnavailable(ctx, log, label, models.UserContactTypeWebPush, route)
	}

	return classifySend(ctx, log, label, models.UserContactTypeWebPush, route,
		deps.SendWebPush(ctx, route.Contact.Value, notice.Subject, ShortBody(notice), notice.URL))
}

// sendSMS texts the compact form of the notice. An unverified number is never
// contacted — same rule as escalation paging.
func sendSMS(
	ctx context.Context, deps Deps, log *slog.Logger, label string,
	route *models.UserNotificationRoute, notice *Notice,
) string {
	if route.Contact.VerifiedAt == nil {
		log.WarnContext(ctx, label+" not sent: phone contact not verified; skipping route",
			"contactUid", route.Contact.UID)

		return outcomeSkipped
	}

	if deps.SendSMS == nil {
		return skipUnavailable(ctx, log, label, models.UserContactTypePhone, route)
	}

	return classifySend(ctx, log, label, models.UserContactTypePhone, route,
		deps.SendSMS(ctx, route.OrgUID, route.Contact.Value, SMSBody(notice)))
}

// ShortBody is the first non-empty content line of a notice — what a push
// notification body has room for.
//
// The "Generated at " skip is inherited from the watchdog digest, whose first
// line is a timestamp nobody needs on a lock screen. It is harmless for every
// other event, and dropping it would regress the digest.
func ShortBody(notice *Notice) string {
	for _, line := range strings.Split(notice.Body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == notice.Subject || strings.HasPrefix(trimmed, "Generated at ") {
			continue
		}

		return trimmed
	}

	return notice.Subject
}

// SMSBody renders a notice for a 160-character medium: the subject, the
// leading content line, and the opt-out footer every outbound SMS carries.
func SMSBody(notice *Notice) string {
	body := notice.Subject + " " + ShortBody(notice)
	if len(body) > smsMaxLen {
		body = body[:smsMaxLen] + "…"
	}

	return body + twilio.OptOutFooter
}

// count records one delivery outcome. A silent drop is the failure mode this
// whole package exists to make visible, so every path through dispatchRoute
// increments exactly once.
func count(event, contactType, outcome string) {
	prommetrics.OperatorNotice.WithLabelValues(event, contactType, outcome).Inc()
}
