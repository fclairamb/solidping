package jobtypes

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/whatsapp"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// whatsAppDetailCap bounds the free-text detail body variable. Meta rejects
// oversized template parameters, and a template variable is not a log line.
const whatsAppDetailCap = 160

// whatsAppButtonParamCap bounds the URL-button suffix for the same reason
// whatsAppDetailCap bounds the detail: Meta rejects an oversized template
// parameter, and that rejection fails the *entire* send — the alert would never
// reach the pager. A real slug + UID sits far below this, so the cap only ever
// fires on corrupt input.
const whatsAppButtonParamCap = 128

// pageWhatsApp delivers an escalation alert to a user's verified WhatsApp
// contact through the instance-level WABA. It mirrors pagePhone:
//
//   - an unverified contact is never messaged (the verification round-trip IS
//     Meta's required opt-in record);
//   - an instance with WhatsApp off degrades to an info-log skip, exactly like a
//     missing Twilio connection does for SMS;
//   - the per-org monthly quota + hourly runaway guard are reserved before the
//     send;
//   - a send failure returns 0 so the escalation step falls through to the next
//     step, precisely as an SMS failure does. A paging channel that swallowed
//     its own failure would be worse than no channel at all.
//
// The alert also carries a URL button pointing at the check's dashboard page,
// so the recipient is one tap from what broke instead of hunting for it. The
// button is best-effort by construction: an unresolvable org slug leaves
// ButtonParam empty, and an empty ButtonParam emits no button component — which
// is exactly what an installation still running the older, buttonless approved
// template needs.
func (r *EscalationStepJobRun) pageWhatsApp(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	incident *models.Incident, route *models.UserNotificationRoute, filter map[string]bool,
) int {
	if !severityAllowsWhatsApp(filter) {
		return 0
	}

	contact := route.Contact

	if contact.VerifiedAt == nil {
		log.InfoContext(ctx, "whatsapp contact not verified; skipping route",
			"contactUID", contact.UID, "userUID", route.UserUID)

		return 0
	}

	if jctx.AppConfig == nil || !jctx.AppConfig.WhatsApp.Active() {
		log.InfoContext(ctx, "whatsapp not configured on this instance; skipping route",
			"contactUID", contact.UID, "orgUID", incident.OrganizationUID)

		return 0
	}

	cfg := &jctx.AppConfig.WhatsApp

	client, err := whatsapp.NewClientFromConfig(cfg)
	if err != nil {
		log.InfoContext(ctx, "whatsapp client unavailable; skipping route",
			"contactUID", contact.UID, "error", err)

		return 0
	}

	if !r.reservePhoneChannel(ctx, jctx, log, incident, contact.Value, models.UsageCounterKindWhatsApp) {
		return 0
	}

	orgSlug := r.orgSlugFor(ctx, jctx, log, incident.OrganizationUID)

	buttonParam := whatsAppCheckButtonParam(orgSlug, incident.CheckUID)
	if buttonParam == "" {
		log.InfoContext(ctx, "whatsapp alert has no check link; sending without the button",
			"incidentUID", incident.UID, "orgUID", incident.OrganizationUID)
	}

	messageID, err := client.SendTemplate(ctx, &whatsapp.TemplateMessage{
		To:       contact.Value,
		Template: cfg.ResolvedAlertTemplate(),
		Language: cfg.ResolvedTemplateLanguage(),
		BodyParams: []string{
			r.phoneCheckName(ctx, jctx, incident),
			whatsAppStateLabel(incident),
			r.whatsAppDetail(ctx, jctx, incident),
			orgSlug,
		},
		ButtonParam: buttonParam,
	})
	if err != nil {
		// Every failure class is handled: log it with the stable reason string
		// and record a skipped audit row so the delivery history shows *why*
		// (e.g. "recipient not on WhatsApp"), then let the escalation continue.
		log.WarnContext(ctx, "failed to send escalation WhatsApp message",
			"contactUID", contact.UID, "reason", whatsapp.FailureReason(err), "error", err)
		r.auditPhoneSkip(ctx, jctx, log, incident, whatsapp.FailureReason(err))

		return 0
	}

	r.auditPhoneSend(ctx, jctx, log, incident, route.UserUID, models.UsageCounterKindWhatsApp, messageID)

	return 1
}

// whatsAppCheckButtonParam builds the URL-button variable of the alert
// template: the path Meta appends to the static prefix fixed when the template
// was approved, documented as `{baseURL}/dash0/`. The result therefore resolves
// to the check page, `/dash0/orgs/{org}/checks/{checkUid}`.
//
// The base URL deliberately lives in the approved template rather than in
// config: a WhatsApp URL button takes a fixed prefix plus one variable, so a
// template is not portable across installations and each one creates its own
// (see web/docs/docs/configuration/whatsapp.md).
//
// Returns "" when the org slug or check UID is unknown, which makes the caller
// send with no button at all — a link pointing at the wrong place is worse than
// no link.
func whatsAppCheckButtonParam(orgSlug, checkUID string) string {
	orgSlug = strings.TrimSpace(orgSlug)
	checkUID = strings.TrimSpace(checkUID)

	if orgSlug == "" || checkUID == "" {
		return ""
	}

	// Slugs and UIDs are already path-safe; escape anyway, because a stray "/"
	// or space would produce a silently broken button rather than an error.
	suffix := "orgs/" + url.PathEscape(orgSlug) + "/checks/" + url.PathEscape(checkUID)

	if len(suffix) > whatsAppButtonParamCap {
		suffix = trimPartialEscape(suffix[:whatsAppButtonParamCap])
	}

	return suffix
}

// trimPartialEscape drops a trailing incomplete percent-escape left behind by a
// byte-wise truncation: "%4" is not a valid escape and would break the URL.
func trimPartialEscape(suffix string) string {
	for i := len(suffix) - 1; i >= 0 && i >= len(suffix)-2; i-- {
		if suffix[i] == '%' {
			return suffix[:i]
		}
	}

	return suffix
}

// whatsAppStateLabel renders the incident's current state as the template's
// state variable. One approved template covers down / escalated / resolved
// precisely because this is a variable rather than three templates.
func whatsAppStateLabel(incident *models.Incident) string {
	switch {
	case incident.State == models.IncidentStateResolved:
		return "RESOLVED"
	case incident.EscalatedAt != nil:
		return "ESCALATED"
	default:
		return "DOWN"
	}
}

// whatsAppDetail builds the free-text detail line: the incident title when it
// has one, otherwise how long it has been open. Capped, single-line, and never
// carrying credentials — it is a template parameter, not a log.
func (r *EscalationStepJobRun) whatsAppDetail(
	_ context.Context, _ *jobdef.JobContext, incident *models.Incident,
) string {
	detail := ""
	if incident.Title != nil {
		detail = strings.TrimSpace(*incident.Title)
	}

	if detail == "" {
		since := time.Since(incident.StartedAt).Round(time.Minute)
		detail = fmt.Sprintf("open for %s", since)
	}

	// Template parameters may not contain newlines or tabs.
	detail = strings.Join(strings.Fields(detail), " ")

	if len(detail) > whatsAppDetailCap {
		detail = detail[:whatsAppDetailCap-1] + "…"
	}

	return detail
}
