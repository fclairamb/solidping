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
	case eventTypeIncidentCreated, eventTypeIncidentResolved, eventTypeIncidentEscalated, eventTypeIncidentReopened:
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

// Event type constants.
const (
	eventTypeIncidentCreated   = "incident.created"
	eventTypeIncidentResolved  = "incident.resolved"
	eventTypeIncidentEscalated = "incident.escalated"
	eventTypeIncidentReopened  = "incident.reopened"
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
	default:
		return "", false
	}
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
		Recipients: email.Recipients{To: emailAddresses},
		Subject:    content.subject,
		HTML:       content.htmlBody,
		Text:       content.textBody,
	}

	res, sendErr := jctx.Services.EmailSender.Send(ctx, msg)
	del.record(emailAddresses, res)

	return sendErr
}

// sendPerRecipient sends one email per recipient with a personalized magic-
// link ack URL (ackable events only) and unsubscribe token (D4, wired in a
// follow-up commit). The first send error short-circuits — the job runner
// will retry the whole batch, which is fine because email delivery itself is
// idempotent at the recipient level (worst case: a duplicate notification).
// Each send's outcome is recorded into del before a failure short-circuits, so
// the audit row still reflects the recipients that were delivered.
func (s *EmailSender) sendPerRecipient(
	ctx context.Context,
	jctx *jobdef.JobContext,
	payload *Payload,
	recipients []string,
	prefix string,
	del *emailDelivery,
) error {
	exp := payload.Incident.StartedAt.Add(ackTokenTTL)
	secret := []byte(jctx.AppConfig.Auth.JWTSecret)
	baseURL := jctx.AppConfig.Server.BaseURL

	for _, recipient := range recipients {
		var ackURL string
		if canAckEvent(payload.EventType) {
			ackURL = buildAckURL(baseURL, payload.OrgSlug, payload.Incident.UID, recipient, secret, exp)
		}

		content, err := s.buildEmailContent(jctx, payload, ackURL, recipient)
		if err != nil {
			return fmt.Errorf("building email content for %s: %w", recipient, err)
		}

		if prefix != "" {
			content.subject = prefix + " " + content.subject
		}

		msg := &email.Message{
			Recipients: email.Recipients{To: []string{recipient}},
			Subject:    content.subject,
			HTML:       content.htmlBody,
			Text:       content.textBody,
		}

		res, sendErr := jctx.Services.EmailSender.Send(ctx, msg)
		del.record([]string{recipient}, res)

		if sendErr != nil {
			return sendErr
		}
	}

	return nil
}

type emailContent struct {
	subject  string
	htmlBody string
	textBody string
}

// buildEmailContent assembles the view-model for one recipient and renders it
// through the shared formatter — the one rendering pipeline for every email
// (D1). recipientEmail is only used for future unsubscribe-token generation
// (D4); it may be "" for the broadcast (non-modeled-event) path.
func (s *EmailSender) buildEmailContent(
	jctx *jobdef.JobContext, payload *Payload, ackURL, recipientEmail string,
) (emailContent, error) {
	checkName := "Unknown check"
	if payload.Check.Name != nil {
		checkName = *payload.Check.Name
	} else if payload.Check.Slug != nil {
		checkName = *payload.Check.Slug
	}

	templateName, ok := incidentTemplateForEvent(payload.EventType)
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

	viewModel := s.buildIncidentViewModel(checkName, payload, ackURL, recipientEmail)

	subject, html, text, err := jctx.Services.EmailFormatter.Format(templateName, viewModel)
	if err != nil {
		return emailContent{}, fmt.Errorf("formatting template %s: %w", templateName, err)
	}

	return emailContent{subject: subject, htmlBody: html, textBody: text}, nil
}

// buildIncidentViewModel builds the data map passed to the incident-*.html
// templates: check/incident details, dashboard deep links (D3), and the ack
// URL (when the event is ackable). recipientEmail and the unsubscribe fields
// are threaded through for D4 (wired in a follow-up commit); left as zero
// values here has no visible effect since the templates render their
// unsubscribe block conditionally on UnsubscribeURL being non-empty.
func (s *EmailSender) buildIncidentViewModel(
	checkName string, payload *Payload, ackURL, recipientEmail string,
) map[string]any {
	_ = recipientEmail // used once D4's unsubscribe-token wiring lands

	vm := map[string]any{
		"CheckName":    checkName,
		"CheckType":    payload.Check.Type,
		"CheckURL":     checkDashURL(payload.AppBaseURL, payload.OrgSlug, payload.Check),
		"StartedAt":    payload.Incident.StartedAt.Format("2006-01-02 15:04:05"),
		"IncidentUID":  payload.Incident.UID,
		"IncidentURL":  incidentDashURL(payload.AppBaseURL, payload.OrgSlug, payload.Incident),
		"AckURL":       ackURL,
		"FailureCount": payload.Incident.FailureCount,
		"RelapseCount": payload.Incident.RelapseCount,
		"DashboardURL": dashboardRootURL(payload.AppBaseURL),
		"DocsURL":      docsURL(payload.AppBaseURL),
	}

	if payload.Incident.ResolvedAt != nil {
		vm["ResolvedAt"] = payload.Incident.ResolvedAt.Format("2006-01-02 15:04:05")
		vm["Duration"] = payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt).Round(time.Second).String()
	}

	return vm
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
