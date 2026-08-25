package notifications

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/incidentlinks"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// ackTokenTTL mirrors the spec for magic-link tokens — valid for 7 days from
// incident start. Kept short enough to limit the impact of a leaked email.
const ackTokenTTL = 7 * 24 * time.Hour

// unsubscribeTokenTTL is the spec's 90-day TTL for unsubscribe links —
// longer than ack's 7 days because these get clicked long after delivery
// (a recipient may only notice and act on a flood of alerts weeks later).
const unsubscribeTokenTTL = 90 * 24 * time.Hour

// canAckEvent reports whether the event type produces an actionable incident
// in the dashboard — only those events get a magic-link "acknowledge" button
// in the email body. A resolved incident has nothing to ack.
func canAckEvent(eventType string) bool {
	switch eventType {
	case eventTypeIncidentCreated, eventTypeIncidentEscalated, eventTypeIncidentReopened:
		return true
	default:
		return false
	}
}

// isModeledIncidentEvent reports whether eventType is one of the four
// incident lifecycle events that ship a dedicated template
// (incident-created/resolved/escalated/reopened.html). All four now send
// one personalized email per recipient — even "resolved", which used to
// broadcast — because every recipient needs their own signed unsubscribe
// token (D4) regardless of whether the event is ackable.
func isModeledIncidentEvent(eventType string) bool {
	switch eventType {
	case eventTypeIncidentCreated, eventTypeIncidentResolved, eventTypeIncidentEscalated,
		eventTypeIncidentReopened, eventTypeIncidentComment, eventTypeIncidentAcknowledged:
		return true
	default:
		return false
	}
}

// buildAckURL composes the magic-link URL for a recipient. Returns "" when
// any of the inputs needed to make a usable URL are missing — the caller
// then falls back to the unpersonalized email body.
func buildAckURL(baseURL, orgSlug, incidentUID, recipientEmail string, secret []byte, exp time.Time) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" || orgSlug == "" || incidentUID == "" || recipientEmail == "" || len(secret) == 0 {
		return ""
	}

	token := incidentlinks.Sign(secret, incidentUID, recipientEmail, exp)

	return fmt.Sprintf("%s/api/v1/orgs/%s/incidents/%s/ack?token=%s",
		baseURL, orgSlug, incidentUID, token)
}

// buildUnsubscribeURL composes the per-recipient unsubscribe URL (D4).
// checkUID scopes the token to one check — every incident email is
// check-scoped by construction, since it always concerns one specific
// check's incident; the org-wide "all alerts" scope is only reachable by
// widening from the confirmation page (unsubscribe.ScopeOrg), not from the
// token an email carries. Returns "" when any required input is missing, so
// the caller can omit both the footer link and the List-Unsubscribe header
// rather than rendering/sending a broken one.
func buildUnsubscribeURL(baseURL, orgSlug, recipientEmail, checkUID string, secret []byte, exp time.Time) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" || orgSlug == "" || recipientEmail == "" || checkUID == "" || len(secret) == 0 {
		return ""
	}

	token := incidentlinks.SignUnsubscribe(secret, orgSlug, recipientEmail, checkUID, exp)

	return fmt.Sprintf("%s/unsubscribe?token=%s", baseURL, token)
}

// Event type constants.
const (
	eventTypeIncidentCreated   = "incident.created"
	eventTypeIncidentResolved  = "incident.resolved"
	eventTypeIncidentEscalated = "incident.escalated"
	eventTypeIncidentReopened  = "incident.reopened"
	eventTypeIncidentComment   = "incident.comment"
	// eventTypeIncidentAcknowledged closes the loop with everyone who was
	// paged: without it a teammate taking the incident is invisible to every
	// channel except the one the button lived in.
	eventTypeIncidentAcknowledged = "incident.acknowledged"
)

// incidentTemplateForEvent maps an event type to its embedded template name.
// Returns "", false for event types with no dedicated template (the
// generic/unrecognized fallback keeps its ad-hoc body — that path is not one
// of the four modeled events and carries no ack/unsubscribe semantics).
func incidentTemplateForEvent(eventType string) (string, bool) {
	switch eventType {
	case eventTypeIncidentCreated:
		return "incident-created.html", true
	case eventTypeIncidentResolved:
		return "incident-resolved.html", true
	case eventTypeIncidentEscalated:
		return "incident-escalated.html", true
	case eventTypeIncidentReopened:
		return "incident-reopened.html", true
	case eventTypeIncidentComment:
		return "incident-comment.html", true
	case eventTypeIncidentAcknowledged:
		return "incident-acknowledged.html", true
	default:
		return "", false
	}
}

// burnTemplateForEvent picks the burn-specific template for an SLO burn-rate
// incident. Returns false for every other incident, leaving the ordinary
// check-outage templates in place.
func burnTemplateForEvent(incident *models.Incident, eventType string) (string, bool) {
	if incident == nil || incident.Kind != models.IncidentKindSLOBurn {
		return "", false
	}

	switch eventType {
	case eventTypeIncidentCreated, eventTypeIncidentReopened:
		return "incident-burn-created.html", true
	case eventTypeIncidentResolved:
		return "incident-burn-resolved.html", true
	default:
		// Escalations and comments keep the generic incident templates: they
		// are about the paging cycle, not about the burn numbers.
		return "", false
	}
}

// applyBurnViewModel adds the burn-specific keys the burn templates render.
// The three that matter — rate, budget remaining, projected exhaustion — are
// the ones that let a reader decide without opening the dashboard.
func applyBurnViewModel(viewModel map[string]any, burn *BurnInfo) {
	viewModel["SLOName"] = burn.SLOName
	viewModel["BurnPolicyLabel"] = burn.PolicyLabel
	viewModel["BurnSeverity"] = burn.Severity
	viewModel["BurnRate"] = burn.RateText()
	viewModel["BurnShortRate"] = burn.ShortRateText()
	viewModel["BurnPeakRate"] = fmt.Sprintf("%.1fx", burn.PeakRate)
	viewModel["BurnThreshold"] = burn.ThresholdText()
	viewModel["BurnLongWindow"] = humanDuration(burn.LongWindow)
	viewModel["BurnShortWindow"] = humanDuration(burn.ShortWindow)
	viewModel["BurnBudgetRemaining"] = burn.BudgetRemainingText()
	viewModel["BurnProjectedExhaustion"] = burn.ProjectedExhaustionText()
	viewModel["BurnTarget"] = fmt.Sprintf("%.3g%%", burn.TargetPct)
}

var (
	// ErrEmailSenderNotConfigured is returned when the email sender service is not available.
	ErrEmailSenderNotConfigured = errors.New("email sender not configured")
	// ErrEmailFormatterNotConfigured is returned when the email formatter service is not available.
	ErrEmailFormatterNotConfigured = errors.New("email formatter not configured")
	// ErrNoRecipientsConfigured is returned when no recipients are configured for email notifications.
	ErrNoRecipientsConfigured = errors.New("no recipients configured")
	// ErrNoValidRecipients is returned when all configured recipients are invalid.
	ErrNoValidRecipients = errors.New("no valid recipients")
)

// EmailSender sends notifications via email.
type EmailSender struct{}

// extractRecipients resolves the email recipient list from an integration's
// "to" setting. It is defensive about the underlying value type: a JSONB load
// yields []any, while in-memory construction (tests, future callers) may yield
// []string. Both are accepted, with element-level string filtering so non-string
// entries are skipped.
//
// The boolean return reports whether the "to" key was present and non-empty —
// distinguishing "no recipients configured at all" (ok=false →
// ErrNoRecipientsConfigured) from "configured but no element is a usable string"
// (ok=true, len==0 → ErrNoValidRecipients).
func extractRecipients(raw any) ([]string, bool) {
	switch list := raw.(type) {
	case []any:
		if len(list) == 0 {
			return nil, false
		}

		out := make([]string, 0, len(list))
		for _, r := range list {
			if addr, ok := r.(string); ok && addr != "" {
				out = append(out, addr)
			}
		}

		return out, true
	case []string:
		if len(list) == 0 {
			return nil, false
		}

		out := make([]string, 0, len(list))
		for _, addr := range list {
			if addr != "" {
				out = append(out, addr)
			}
		}

		return out, true
	default:
		return nil, false
	}
}

// emailDelivery accumulates the outcome of one or more SMTP sends for a single
// notification audit row. A notification to an email integration fans out to one
// send per recipient (every modeled incident event now sends per-recipient, so
// each carries its own ack/unsubscribe tokens); either way we surface the first
// RFC Message-ID on the row (as a correlation handle) and a transcript of every
// send — recipients, Message-ID, and the SMTP server's queue response — in the
// delivery details.
type emailDelivery struct {
	firstMessageID string
	transcript     []string
}

// record appends one send's outcome. res is nil when Send errored before
// producing a result; a no-op (Sent=false, e.g. email disabled) is also skipped
// so a disabled-email notification produces no misleading delivery artifacts.
func (d *emailDelivery) record(recipients []string, res *email.SendResult) {
	if res == nil || !res.Sent {
		return
	}

	if d.firstMessageID == "" {
		d.firstMessageID = res.MessageID
	}

	var entry strings.Builder
	fmt.Fprintf(&entry, "recipients: %s", strings.Join(recipients, ", "))

	if res.MessageID != "" {
		fmt.Fprintf(&entry, "\nmessage-id: %s", res.MessageID)
	}

	if res.ServerResponse != "" {
		fmt.Fprintf(&entry, "\nserver: %s", res.ServerResponse)
	}

	d.transcript = append(d.transcript, entry.String())
}

// apply writes the accumulated identifiers and transcript onto the payload so
// the job runner persists them on the audit row. Called via defer so a partial
// batch (some recipients delivered, then a failure) still surfaces what got
// through. elapsed is the wall-clock time of the whole send attempt.
func (d *emailDelivery) apply(payload *Payload, elapsed time.Duration) {
	if len(d.transcript) == 0 {
		return
	}

	payload.MessageID = d.firstMessageID
	payload.DeliveryDetails = &models.DeliveryDetails{
		DurationMs:   elapsed.Milliseconds(),
		ResponseBody: models.CapBody([]byte(strings.Join(d.transcript, "\n\n"))),
	}
}

// Send sends a notification via email.
func (s *EmailSender) Send(ctx context.Context, jctx *jobdef.JobContext, payload *Payload) error {
	if jctx.Services.EmailSender == nil {
		return ErrEmailSenderNotConfigured
	}

	if jctx.Services.EmailFormatter == nil {
		return ErrEmailFormatterNotConfigured
	}

	// Extract recipients from the canonical "to" settings key. The dashboard
	// writes recipients under "to", and the rest of the email subsystem uses
	// the same key — so "to" is the canonical storage key.
	emailAddresses, ok := extractRecipients(payload.Integration.Settings["to"])
	if !ok {
		return ErrNoRecipientsConfigured
	}

	if len(emailAddresses) == 0 {
		return ErrNoValidRecipients
	}

	prefix, _ := payload.Integration.Settings["subject_prefix"].(string)

	// Surface what we actually sent on the audit row — the first Message-ID and a
	// per-send transcript with each SMTP server response — regardless of how the
	// send below returns (success, or a partial batch that then failed).
	del := &emailDelivery{}
	start := time.Now()

	defer func() { del.apply(payload, time.Since(start)) }()

	// Every modeled incident event (created/resolved/escalated/reopened) now
	// sends one personalized email per recipient — each recipient needs their
	// own signed unsubscribe token (D4), and ackable events additionally get
	// their own ack link. Unrecognized event types keep the legacy single
	// broadcast (dead code today; kept as a defensive fallback).
	if isModeledIncidentEvent(payload.EventType) {
		return s.sendPerRecipient(ctx, jctx, payload, emailAddresses, prefix, del)
	}

	content, err := s.buildEmailContent(jctx, payload, "", "")
	if err != nil {
		return fmt.Errorf("building email content: %w", err)
	}

	if prefix != "" {
		content.subject = prefix + " " + content.subject
	}

	msg := &email.Message{
		Recipients:       email.Recipients{To: emailAddresses},
		Subject:          content.subject,
		HTML:             content.htmlBody,
		Text:             content.textBody,
		SupportReplyable: email.SupportReplyable(content.template),
	}

	res, sendErr := jctx.Services.EmailSender.Send(ctx, msg)
	del.record(emailAddresses, res)

	return sendErr
}

// sendPerRecipient sends one email per recipient with a personalized magic-
// link ack URL (ackable events only) and a per-recipient signed unsubscribe
// link (D4). Suppressed (org, email, check) combinations are filtered out
// before sending — each gets a `skipped` incident_notifications audit row
// instead (spec acceptance criterion 4) — so a recipient who unsubscribed
// never receives another alert for that check, while the rest of the
// recipient list is unaffected.
//
// The first send error to a NON-suppressed recipient short-circuits — the
// job runner will retry the whole batch, which is fine because email
// delivery itself is idempotent at the recipient level (worst case: a
// duplicate notification). Each send's outcome is recorded into del before a
// failure short-circuits, so the audit row still reflects the recipients
// that were delivered.
func (s *EmailSender) sendPerRecipient(
	ctx context.Context,
	jctx *jobdef.JobContext,
	payload *Payload,
	recipients []string,
	prefix string,
	del *emailDelivery,
) error {
	ackExp := payload.Incident.StartedAt.Add(ackTokenTTL)
	unsubExp := payload.Incident.StartedAt.Add(unsubscribeTokenTTL)
	secret := []byte(jctx.AppConfig.Auth.JWTSecret)
	baseURL := jctx.AppConfig.Server.BaseURL

	for _, recipient := range recipients {
		suppressed, err := s.isRecipientSuppressed(ctx, jctx, payload, recipient)
		if err != nil {
			return fmt.Errorf("checking suppression for %s: %w", recipient, err)
		}

		if suppressed {
			s.recordSkippedForSuppression(ctx, jctx, payload, recipient)

			continue
		}

		var ackURL string
		if canAckEvent(payload.EventType) {
			ackURL = buildAckURL(baseURL, payload.OrgSlug, payload.Incident.UID, recipient, secret, ackExp)
		}

		unsubURL := buildUnsubscribeURL(baseURL, payload.OrgSlug, recipient, payload.Check.UID, secret, unsubExp)

		content, err := s.buildEmailContent(jctx, payload, ackURL, unsubURL)
		if err != nil {
			return fmt.Errorf("building email content for %s: %w", recipient, err)
		}

		if prefix != "" {
			content.subject = prefix + " " + content.subject
		}

		msg := &email.Message{
			Recipients:                  email.Recipients{To: []string{recipient}},
			Subject:                     content.subject,
			HTML:                        content.htmlBody,
			Text:                        content.textBody,
			ListUnsubscribeURL:          unsubURL,
			ListUnsubscribePostOneClick: unsubURL != "",
			SupportReplyable:            email.SupportReplyable(content.template),
		}

		res, sendErr := jctx.Services.EmailSender.Send(ctx, msg)
		del.record([]string{recipient}, res)

		if sendErr != nil {
			return sendErr
		}
	}

	return nil
}

// isRecipientSuppressed reports whether recipient has unsubscribed from
// alerts for this check (either a check-specific or an org-wide
// suppression). Returns false, nil when jctx.DBService is nil (test doubles
// that don't wire a DB service) so existing tests without suppression
// fixtures keep sending — production always has a DBService.
func (s *EmailSender) isRecipientSuppressed(
	ctx context.Context, jctx *jobdef.JobContext, payload *Payload, recipient string,
) (bool, error) {
	if jctx.DBService == nil {
		return false, nil
	}

	suppressed, err := jctx.DBService.IsEmailSuppressed(
		ctx, payload.Integration.OrganizationUID, recipient, payload.Check.UID)
	if err != nil {
		return false, fmt.Errorf("is email suppressed: %w", err)
	}

	return suppressed, nil
}

// recordSkippedForSuppression inserts a `skipped` incident_notifications
// audit row naming the suppression, so the dashboard's notification history
// explains why this recipient didn't get an email (spec acceptance
// criterion 4). Best-effort: a failure to write the audit row logs but does
// not fail the overall send — the recipient is still correctly skipped
// either way, and an audit-row write failure shouldn't block delivery to
// the remaining recipients.
func (s *EmailSender) recordSkippedForSuppression(
	ctx context.Context, jctx *jobdef.JobContext, payload *Payload, recipient string,
) {
	if jctx.DBService == nil {
		return
	}

	skipReason := fmt.Sprintf("suppressed: %s unsubscribed from alerts for this check", recipient)
	row := models.NewSkippedIncidentNotification(
		payload.Integration.OrganizationUID,
		payload.Incident.UID,
		payload.EventType,
		models.IncidentNotificationSourceCheckConnection,
		skipReason,
		nil, nil,
	)

	if err := jctx.DBService.CreateIncidentNotification(ctx, row); err != nil {
		jctx.Logger.WarnContext(ctx, "failed to record skipped-for-suppression audit row",
			"recipient", recipient, "incidentUid", payload.Incident.UID, "error", err)
	}
}

type emailContent struct {
	subject  string
	htmlBody string
	textBody string
	// template is the rendered template's file name, carried out so the caller
	// can ask email.SupportReplyable about it. Empty on the ad-hoc fallback
	// body below, which therefore fails closed — see spec 2026-08-22-02.
	template string
}

// buildEmailContent assembles the view-model for one recipient and renders it
// through the shared formatter — the one rendering pipeline for every email
// (D1). unsubURL is "" for the broadcast (non-modeled-event) path, which has
// no per-recipient token to offer.
func (s *EmailSender) buildEmailContent(
	jctx *jobdef.JobContext, payload *Payload, ackURL, unsubURL string,
) (emailContent, error) {
	checkName := "Unknown check"
	if payload.Check.Name != nil {
		checkName = *payload.Check.Name
	} else if payload.Check.Slug != nil {
		checkName = *payload.Check.Slug
	}

	templateName, ok := incidentTemplateForEvent(payload.EventType)

	// A burn alert rides the SAME incident.created / incident.resolved events
	// a check outage does, so the event type alone cannot pick the template:
	// "[DOWN] api is down" would be an outright lie about an objective that
	// merely spent budget too fast. The incident's kind is the discriminator.
	if burnName, burnOK := burnTemplateForEvent(payload.Incident, payload.EventType); burnOK {
		templateName, ok = burnName, true
	}

	if !ok {
		// Unrecognized event type: no dedicated template exists. Keep a
		// minimal ad-hoc body rather than failing the whole notification —
		// this path is unreachable from the four modeled events emitted by
		// the incident state machine today, so it only guards against a
		// future new event type shipping without a template.
		return emailContent{
			subject:  fmt.Sprintf("[SolidPing] %s incident update", checkName),
			htmlBody: fmt.Sprintf("<p>Incident update for <strong>%s</strong>: %s</p>", checkName, payload.EventType),
			textBody: fmt.Sprintf("Incident update for %s: %s", checkName, payload.EventType),
		}, nil
	}

	viewModel := s.buildIncidentViewModel(checkName, payload, ackURL, unsubURL)

	subject, html, text, err := jctx.Services.EmailFormatter.Format(templateName, viewModel)
	if err != nil {
		return emailContent{}, fmt.Errorf("formatting template %s: %w", templateName, err)
	}

	return emailContent{subject: subject, htmlBody: html, textBody: text, template: templateName}, nil
}

// buildIncidentViewModel builds the data map passed to the incident-*.html
// templates: check/incident details, dashboard deep links (D3), the ack URL
// (when the event is ackable), and the unsubscribe footer link (D4).
// unsubURL is "" for the broadcast (non-modeled-event) path — the templates
// render their unsubscribe footer line conditionally on UnsubscribeURL being
// non-empty, so an empty string simply omits that line.
func (s *EmailSender) buildIncidentViewModel(
	checkName string, payload *Payload, ackURL, unsubURL string,
) map[string]any {
	viewModel := map[string]any{
		"CheckName":      checkName,
		"CheckType":      payload.Check.Type,
		"CheckURL":       checkDashURL(payload.AppBaseURL, payload.OrgSlug, payload.Check),
		"StartedAt":      payload.Incident.StartedAt.Format("2006-01-02 15:04:05"),
		"IncidentUID":    payload.Incident.UID,
		"IncidentNumber": payload.Incident.Number,
		"IncidentURL":    incidentDashURL(payload.AppBaseURL, payload.OrgSlug, payload.Incident),
		"AckURL":         ackURL,
		"FailureCount":   payload.Incident.FailureCount,
		"RelapseCount":   payload.Incident.RelapseCount,
		"DashboardURL":   dashboardRootURL(payload.AppBaseURL),
		"DocsURL":        docsURL(payload.AppBaseURL),
	}

	if payload.Incident.ResolvedAt != nil {
		viewModel["ResolvedAt"] = payload.Incident.ResolvedAt.Format("2006-01-02 15:04:05")
		viewModel["Duration"] = payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt).Round(time.Second).String()
	}

	if burn := BurnInfoFor(payload.Incident); burn != nil {
		applyBurnViewModel(viewModel, burn)
	}

	if payload.Comment != nil {
		viewModel["CommentText"] = commentText(payload.Comment)
		viewModel["CommentAuthor"] = commentAuthor(payload.Comment)
		viewModel["CommentSource"] = commentSourceLabel(payload.Comment)
	}

	if payload.EventType == eventTypeIncidentAcknowledged {
		viewModel["AckActor"] = ackActor(payload.Acknowledgment)
		viewModel["AckVia"] = ackViaLabel(payload.Acknowledgment)
	}

	if payload.Incident.AcknowledgedAt != nil {
		viewModel["AcknowledgedAt"] = payload.Incident.AcknowledgedAt.Format("2006-01-02 15:04:05")
	}

	if unsubURL != "" {
		viewModel["UnsubscribeURL"] = unsubURL
		viewModel["UnsubscribeCheckName"] = checkName
	}

	return viewModel
}

// dashboardRootURL returns the dash0 root URL for the email footer link.
// Returns "" when baseURL is empty so the footer omits the link rather than
// rendering a broken one — in practice SP_SERVER_BASE_URL always resolves to
// a default, so this is defensive rather than a real degradation path.
func dashboardRootURL(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return ""
	}

	return baseURL + "/dash0"
}

// docsURL returns the documentation root URL for the email footer link. Same
// empty-baseURL behavior as dashboardRootURL.
func docsURL(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return ""
	}

	return baseURL + "/docs"
}
