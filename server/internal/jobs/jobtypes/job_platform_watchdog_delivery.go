package jobtypes

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	smssvc "github.com/fclairamb/solidping/server/internal/integrations/sms"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
	"github.com/fclairamb/solidping/server/internal/integrations/twilio"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/watchdog"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// watchdogSMSMaxLen bounds the SMS rendering of the digest. A digest is a
// multi-line report; an SMS is not. The subject line carries the counts and
// the operator opens the full one on another route.
const watchdogSMSMaxLen = 300

// deliverWatchdogDigest sends ONE digest per run to every configured
// recipient, through the recipient's OWN notification routes.
//
// No new medium and no hardcoded webhook: operators already maintain their
// contact preferences for incident paging, and the watchdog inherits them. The
// only thing this adds is the guarantee that a drop is never silent — every
// recipient that could not be reached is named in the log, because a silent
// drop on an alerting path is the exact bug this spec exists to kill.
func deliverWatchdogDigest(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	cfg *watchdog.Config, digest watchdog.Digest,
) {
	if len(cfg.Recipients) == 0 {
		// Enabled with nobody to tell. The anomalies are still logged and
		// metered above; this WARN is what makes the misconfiguration visible
		// instead of letting an operator believe they are covered.
		log.WarnContext(ctx,
			"Platform watchdog is enabled but has no recipients; the digest was logged, not delivered",
			"subject", digest.Subject)

		return
	}

	for _, userUID := range cfg.Recipients {
		deliverWatchdogDigestToUser(ctx, jctx, log, userUID, digest)
	}
}

// deliverWatchdogDigestToUser fans one digest out over a recipient's enabled
// routes, in position order — the same ordering incident paging uses.
func deliverWatchdogDigestToUser(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	userUID string, digest watchdog.Digest,
) {
	log = log.With("recipientUid", userUID)

	user, err := jctx.DBService.GetUser(ctx, userUID)
	if err != nil || user == nil {
		log.WarnContext(ctx, "Platform watchdog digest undeliverable: recipient user not found", "error", err)

		return
	}

	routes := watchdogRoutesFor(ctx, jctx, log, userUID)

	delivered := 0
	for _, route := range routes {
		if dispatchWatchdogRoute(ctx, jctx, log, route, digest) {
			delivered++
		}
	}

	if delivered == 0 {
		log.WarnContext(ctx,
			"Platform watchdog digest undeliverable: recipient has no enabled notification route that can carry it",
			"email", user.Email, "routes", len(routes))

		return
	}

	log.InfoContext(ctx, "Platform watchdog digest delivered", "deliveries", delivered)
}

// watchdogRoutesFor collects a recipient's enabled routes across every
// organization they belong to.
//
// Notification routes are org-scoped (a Slack DM needs the org's bot token),
// but the watchdog reports on the PLATFORM and has no org of its own. Walking
// the recipient's memberships is what lets an operator be reached through the
// contacts they already configured, wherever they configured them. Contacts
// are de-duplicated so a person who set the same address up in two orgs is
// messaged once.
func watchdogRoutesFor(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, userUID string,
) []*models.UserNotificationRoute {
	members, err := jctx.DBService.ListMembersByUser(ctx, userUID)
	if err != nil {
		log.WarnContext(ctx, "Could not list the watchdog recipient's organizations", "error", err)

		return nil
	}

	seen := make(map[string]bool)
	out := make([]*models.UserNotificationRoute, 0, len(members))

	for _, member := range members {
		routes, routesErr := jctx.DBService.ListUserContactsWithRoutes(ctx, userUID, member.OrganizationUID)
		if routesErr != nil {
			log.WarnContext(ctx, "Could not list the watchdog recipient's notification routes",
				"orgUid", member.OrganizationUID, "error", routesErr)

			continue
		}

		for _, route := range routes {
			if !route.Enabled || route.Contact == nil || seen[route.Contact.UID] {
				continue
			}

			seen[route.Contact.UID] = true

			out = append(out, route)
		}
	}

	return out
}

// dispatchWatchdogRoute delivers the digest over one route. Returns whether
// the delivery actually went out.
func dispatchWatchdogRoute(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	route *models.UserNotificationRoute, digest watchdog.Digest,
) bool {
	switch route.Contact.Type {
	case models.UserContactTypeEmail:
		return sendWatchdogEmail(ctx, jctx, log, route, digest)
	case models.UserContactTypeTelegram:
		return sendWatchdogTelegram(ctx, jctx, log, route, digest)
	case models.UserContactTypeSlackUser:
		return sendWatchdogSlackDM(ctx, jctx, log, route, digest)
	case models.UserContactTypeWebPush:
		return sendWatchdogWebPush(ctx, jctx, log, route, digest)
	case models.UserContactTypePhone:
		return sendWatchdogSMS(ctx, jctx, log, route, digest)
	default:
		// WhatsApp (template-gated: Meta will not carry free-form business
		// text outside a session) and anything added later. Named in the log
		// rather than dropped, so an operator whose only route is one of these
		// finds out from the logs instead of from a missed outage.
		log.WarnContext(ctx, "Platform watchdog cannot deliver over this contact type; skipping route",
			"contactType", route.Contact.Type, "contactUid", route.Contact.UID)

		return false
	}
}

// sendWatchdogEmail enqueues the digest as a normal email job, so it inherits
// the retry chain and the SMTP configuration every other mail uses.
func sendWatchdogEmail(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	route *models.UserNotificationRoute, digest watchdog.Digest,
) bool {
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		log.WarnContext(ctx, "Job service unavailable; cannot enqueue the watchdog digest email")

		return false
	}

	payload, err := json.Marshal(EmailJobConfig{
		To:      []string{route.Contact.Value},
		Subject: digest.Subject,
		Text:    digest.Text,
	})
	if err != nil {
		log.WarnContext(ctx, "Failed to encode the watchdog digest email", "error", err)

		return false
	}

	if _, err := jctx.Services.Jobs.CreateJob(
		ctx, route.OrgUID, string(jobdef.JobTypeEmail), payload, nil,
	); err != nil {
		log.WarnContext(ctx, "Failed to enqueue the watchdog digest email",
			"contactUid", route.Contact.UID, "error", err)

		return false
	}

	return true
}

// sendWatchdogTelegram DMs the digest through the instance bot.
func sendWatchdogTelegram(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	route *models.UserNotificationRoute, digest watchdog.Digest,
) bool {
	client, ok := telegramClientFor(ctx, jctx, log)
	if !ok {
		return false
	}

	// The whole digest is machine-generated, but it interpolates region slugs
	// and check names, so it goes through the escaper like every other body.
	html := "<b>" + telegram.EscapeHTML(digest.Subject) + "</b>\n<pre>" +
		telegram.EscapeHTML(digest.Text) + "</pre>"

	if _, err := client.SendMessage(ctx, &telegram.Message{
		ChatID: route.Contact.Value,
		HTML:   html,
	}); err != nil {
		log.WarnContext(ctx, "Failed to send the watchdog digest over Telegram",
			"contactUid", route.Contact.UID, "error", err)

		return false
	}

	return true
}

// sendWatchdogSlackDM delivers the digest as a Slack DM through the org's own
// Slack connection — the same path escalation DMs take.
func sendWatchdogSlackDM(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	route *models.UserNotificationRoute, digest watchdog.Digest,
) bool {
	conn, err := jctx.DBService.GetSlackChannelForOrg(ctx, route.OrgUID)
	if err != nil {
		log.WarnContext(ctx, "No Slack connection for the org; skipping the watchdog Slack route",
			"orgUid", route.OrgUID, "contactUid", route.Contact.UID, "error", err)

		return false
	}

	settings, parseErr := models.SlackSettingsFromJSONMap(conn.Settings)
	if parseErr != nil || settings.AccessToken == "" {
		log.WarnContext(ctx, "Slack access token not configured; skipping the watchdog Slack route",
			"orgUid", route.OrgUID, "contactUid", route.Contact.UID)

		return false
	}

	if err := postSlackDM(ctx, settings.AccessToken, route.Contact.Value, digest.Text); err != nil {
		log.WarnContext(ctx, "Failed to send the watchdog digest over Slack",
			"contactUid", route.Contact.UID, "error", err)

		return false
	}

	return true
}

// sendWatchdogWebPush pushes the subject line plus the first anomaly.
func sendWatchdogWebPush(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	route *models.UserNotificationRoute, digest watchdog.Digest,
) bool {
	if jctx.Services == nil || jctx.Services.WebPushOptions.VAPIDPublicKey == "" {
		log.WarnContext(ctx, "Web push not configured; skipping the watchdog web push route",
			"contactUid", route.Contact.UID)

		return false
	}

	err := webpush.Send(ctx, jctx.Services.WebPushOptions, route.Contact.Value, webpush.Message{
		Title: digest.Subject,
		Body:  watchdogShortBody(digest),
	})
	if err != nil {
		log.WarnContext(ctx, "Failed to send the watchdog digest over web push",
			"contactUid", route.Contact.UID, "error", err)

		return false
	}

	return true
}

// sendWatchdogSMS texts the compact form of the digest. An unverified number
// is never contacted — same rule as escalation paging.
func sendWatchdogSMS(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	route *models.UserNotificationRoute, digest watchdog.Digest,
) bool {
	if route.Contact.VerifiedAt == nil {
		log.WarnContext(ctx, "Phone contact not verified; skipping the watchdog SMS route",
			"contactUid", route.Contact.UID)

		return false
	}

	if jctx.Services == nil || jctx.Services.SMS == nil {
		log.WarnContext(ctx, "SMS resolver not wired; skipping the watchdog SMS route",
			"contactUid", route.Contact.UID)

		return false
	}

	resolution, err := jctx.Services.SMS.Resolve(ctx, route.OrgUID)
	if err != nil || resolution == nil || !resolution.SMSAvailable() {
		log.WarnContext(ctx, "No SMS provider available; skipping the watchdog SMS route",
			"orgUid", route.OrgUID, "contactUid", route.Contact.UID, "error", err)

		return false
	}

	body := watchdogSMSBody(digest)

	if _, err := resolution.Sender.SendSMS(ctx, &smssvc.SendParams{
		To:   route.Contact.Value,
		Body: body,
	}); err != nil {
		log.WarnContext(ctx, "Failed to send the watchdog digest over SMS",
			"contactUid", route.Contact.UID, "error", err)

		return false
	}

	return true
}

// watchdogShortBody is the first non-empty content line of the digest — what
// a push notification body has room for.
func watchdogShortBody(digest watchdog.Digest) string {
	for _, line := range strings.Split(digest.Text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == digest.Subject || strings.HasPrefix(trimmed, "Generated at ") {
			continue
		}

		return trimmed
	}

	return digest.Subject
}

// watchdogSMSBody renders the digest for a 160-character medium: the subject,
// the leading anomaly, and the opt-out footer every outbound SMS carries.
func watchdogSMSBody(digest watchdog.Digest) string {
	body := digest.Subject + " " + watchdogShortBody(digest)
	if len(body) > watchdogSMSMaxLen {
		body = body[:watchdogSMSMaxLen] + "…"
	}

	return body + twilio.OptOutFooter
}
