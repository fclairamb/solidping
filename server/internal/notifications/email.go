package notifications

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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

var (
	// ErrEmailSenderNotConfigured is returned when the email sender service is not available.
	ErrEmailSenderNotConfigured = errors.New("email sender not configured")
	// ErrNoRecipientsConfigured is returned when no recipients are configured for email notifications.
	ErrNoRecipientsConfigured = errors.New("no recipients configured")
	// ErrNoValidRecipients is returned when all configured recipients are invalid.
	ErrNoValidRecipients = errors.New("no valid recipients")
)

// EmailSender sends notifications via email.
type EmailSender struct{}

// Send sends a notification via email.
func (s *EmailSender) Send(ctx context.Context, jctx *jobdef.JobContext, payload *Payload) error {
	if jctx.Services.EmailSender == nil {
		return ErrEmailSenderNotConfigured
	}

	// Extract recipients from settings
	recipientList, ok := payload.Integration.Settings["recipients"].([]any)
	if !ok || len(recipientList) == 0 {
		return ErrNoRecipientsConfigured
	}

	emailAddresses := make([]string, 0, len(recipientList))
	for _, r := range recipientList {
		if emailAddr, ok := r.(string); ok {
			emailAddresses = append(emailAddresses, emailAddr)
		}
	}

	if len(emailAddresses) == 0 {
		return ErrNoValidRecipients
	}

	prefix, _ := payload.Integration.Settings["subject_prefix"].(string)

	// For events that produce an actionable incident (created/escalated/
	// reopened), send one personalized email per recipient so each can carry
	// its own one-click ack link. Resolved goes out as a single broadcast —
	// there's nothing to ack.
	if canAckEvent(payload.EventType) {
		return s.sendPerRecipient(ctx, jctx, payload, emailAddresses, prefix)
	}

	content := s.buildEmailContent(payload, "")

	if prefix != "" {
		content.subject = prefix + " " + content.subject
	}

	msg := &email.Message{
		Recipients: email.Recipients{To: emailAddresses},
		Subject:    content.subject,
		HTML:       content.htmlBody,
		Text:       content.textBody,
	}

	_, err := jctx.Services.EmailSender.Send(ctx, msg)

	return err
}

// sendPerRecipient sends one email per recipient with a personalized magic-
// link ack URL. The first send error short-circuits — the job runner will
// retry the whole batch, which is fine because email delivery itself is
// idempotent at the recipient level (worst case: a duplicate notification).
func (s *EmailSender) sendPerRecipient(
	ctx context.Context,
	jctx *jobdef.JobContext,
	payload *Payload,
	recipients []string,
	prefix string,
) error {
	exp := payload.Incident.StartedAt.Add(ackTokenTTL)
	secret := []byte(jctx.AppConfig.Auth.JWTSecret)
	baseURL := jctx.AppConfig.Server.BaseURL

	for _, recipient := range recipients {
		ackURL := buildAckURL(baseURL, payload.OrgSlug, payload.Incident.UID, recipient, secret, exp)
		content := s.buildEmailContent(payload, ackURL)

		if prefix != "" {
			content.subject = prefix + " " + content.subject
		}

		msg := &email.Message{
			Recipients: email.Recipients{To: []string{recipient}},
			Subject:    content.subject,
			HTML:       content.htmlBody,
			Text:       content.textBody,
		}

		if _, err := jctx.Services.EmailSender.Send(ctx, msg); err != nil {
			return err
		}
	}

	return nil
}

type emailContent struct {
	subject  string
	htmlBody string
	textBody string
}

func (s *EmailSender) buildEmailContent(payload *Payload, ackURL string) emailContent {
	checkName := "Unknown check"
	if payload.Check.Name != nil {
		checkName = *payload.Check.Name
	} else if payload.Check.Slug != nil {
		checkName = *payload.Check.Slug
	}

	switch payload.EventType {
	case eventTypeIncidentCreated:
		return emailContent{
			fmt.Sprintf("[DOWN] %s is down", checkName),
			s.buildIncidentCreatedHTML(checkName, payload) + ackHTMLBlock(ackURL),
			s.buildIncidentCreatedText(checkName, payload) + ackTextBlock(ackURL),
		}
	case eventTypeIncidentResolved:
		return emailContent{
			fmt.Sprintf("[RECOVERED] %s is back up", checkName),
			s.buildIncidentResolvedHTML(checkName, payload),
			s.buildIncidentResolvedText(checkName, payload),
		}
	case eventTypeIncidentEscalated:
		return emailContent{
			fmt.Sprintf("[ESCALATED] %s incident escalated", checkName),
			s.buildIncidentEscalatedHTML(checkName, payload) + ackHTMLBlock(ackURL),
			s.buildIncidentEscalatedText(checkName, payload) + ackTextBlock(ackURL),
		}
	case eventTypeIncidentReopened:
		return emailContent{
			fmt.Sprintf("[REOPENED] %s incident reopened (relapse #%d)", checkName, payload.Incident.RelapseCount),
			s.buildIncidentReopenedHTML(checkName, payload) + ackHTMLBlock(ackURL),
			s.buildIncidentReopenedText(checkName, payload) + ackTextBlock(ackURL),
		}
	default:
		return emailContent{
			fmt.Sprintf("[SolidPing] %s incident update", checkName),
			fmt.Sprintf("<p>Incident update for <strong>%s</strong>: %s</p>", checkName, payload.EventType),
			fmt.Sprintf("Incident update for %s: %s", checkName, payload.EventType),
		}
	}
}

// ackHTMLBlock returns the inline HTML for the magic-link ack button.
// Returns "" when ackURL is empty so the body looks identical to before for
// notifications without a link (resolved events, missing config, etc.).
func ackHTMLBlock(ackURL string) string {
	if ackURL == "" {
		return ""
	}

	return fmt.Sprintf(
		`<p><a href="%s" style="background:#1976d2;color:#fff;padding:10px 16px;`+
			`border-radius:4px;text-decoration:none;display:inline-block;">`+
			`Acknowledge incident</a></p>`+
			`<p style="color:#999;font-size:12px;">This link is valid for 7 days. `+
			`Following it confirms you have seen the incident.</p>`,
		ackURL,
	)
}

// ackTextBlock mirrors ackHTMLBlock for the plain-text body.
func ackTextBlock(ackURL string) string {
	if ackURL == "" {
		return ""
	}

	return "\nAcknowledge this incident:\n" + ackURL + "\n(link valid for 7 days)\n"
}

func (s *EmailSender) buildIncidentCreatedHTML(checkName string, payload *Payload) string {
	return fmt.Sprintf(`
<html>
<body style="font-family: Arial, sans-serif;">
<h2 style="color: #d32f2f;">%s is down</h2>
<p>An incident has been detected for <strong>%s</strong>.</p>
<ul>
<li><strong>Check:</strong> %s</li>
<li><strong>Type:</strong> %s</li>
<li><strong>Started at:</strong> %s</li>
<li><strong>Incident ID:</strong> %s</li>
</ul>
<p style="color: #666;">This is an automated notification from SolidPing.</p>
</body>
</html>
`, checkName, checkName, checkName, payload.Check.Type,
		payload.Incident.StartedAt.Format("2006-01-02 15:04:05"),
		payload.Incident.UID)
}

func (s *EmailSender) buildIncidentCreatedText(checkName string, payload *Payload) string {
	return fmt.Sprintf(`%s is down

An incident has been detected for %s.

Check: %s
Type: %s
Started at: %s
Incident ID: %s

This is an automated notification from SolidPing.
`, checkName, checkName, checkName, payload.Check.Type,
		payload.Incident.StartedAt.Format("2006-01-02 15:04:05"),
		payload.Incident.UID)
}

func (s *EmailSender) buildIncidentResolvedHTML(checkName string, payload *Payload) string {
	duration := ""
	if payload.Incident.ResolvedAt != nil {
		d := payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt)
		duration = fmt.Sprintf("<li><strong>Duration:</strong> %s</li>", d.Round(1))
	}

	return fmt.Sprintf(`
<html>
<body style="font-family: Arial, sans-serif;">
<h2 style="color: #388e3c;">%s recovered</h2>
<p>The incident for <strong>%s</strong> has been resolved.</p>
<ul>
<li><strong>Check:</strong> %s</li>
<li><strong>Type:</strong> %s</li>
<li><strong>Started at:</strong> %s</li>
<li><strong>Resolved at:</strong> %s</li>
%s
<li><strong>Incident ID:</strong> %s</li>
</ul>
<p style="color: #666;">This is an automated notification from SolidPing.</p>
</body>
</html>
`, checkName, checkName, checkName, payload.Check.Type,
		payload.Incident.StartedAt.Format("2006-01-02 15:04:05"),
		payload.Incident.ResolvedAt.Format("2006-01-02 15:04:05"),
		duration, payload.Incident.UID)
}

func (s *EmailSender) buildIncidentResolvedText(checkName string, payload *Payload) string {
	duration := ""
	if payload.Incident.ResolvedAt != nil {
		d := payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt)
		duration = fmt.Sprintf("Duration: %s\n", d.Round(1))
	}

	return fmt.Sprintf(`%s recovered

The incident for %s has been resolved.

Check: %s
Type: %s
Started at: %s
Resolved at: %s
%sIncident ID: %s

This is an automated notification from SolidPing.
`, checkName, checkName, checkName, payload.Check.Type,
		payload.Incident.StartedAt.Format("2006-01-02 15:04:05"),
		payload.Incident.ResolvedAt.Format("2006-01-02 15:04:05"),
		duration, payload.Incident.UID)
}

func (s *EmailSender) buildIncidentEscalatedHTML(checkName string, payload *Payload) string {
	return fmt.Sprintf(`
<html>
<body style="font-family: Arial, sans-serif;">
<h2 style="color: #f57c00;">%s incident escalated</h2>
<p>The incident for <strong>%s</strong> has been escalated.</p>
<ul>
<li><strong>Check:</strong> %s</li>
<li><strong>Type:</strong> %s</li>
<li><strong>Started at:</strong> %s</li>
<li><strong>Failure count:</strong> %d</li>
<li><strong>Incident ID:</strong> %s</li>
</ul>
<p style="color: #666;">This is an automated notification from SolidPing.</p>
</body>
</html>
`, checkName, checkName, checkName, payload.Check.Type,
		payload.Incident.StartedAt.Format("2006-01-02 15:04:05"),
		payload.Incident.FailureCount, payload.Incident.UID)
}

func (s *EmailSender) buildIncidentEscalatedText(checkName string, payload *Payload) string {
	return fmt.Sprintf(`%s incident escalated

The incident for %s has been escalated.

Check: %s
Type: %s
Started at: %s
Failure count: %d
Incident ID: %s

This is an automated notification from SolidPing.
`, checkName, checkName, checkName, payload.Check.Type,
		payload.Incident.StartedAt.Format("2006-01-02 15:04:05"),
		payload.Incident.FailureCount, payload.Incident.UID)
}

func (s *EmailSender) buildIncidentReopenedHTML(checkName string, payload *Payload) string {
	return fmt.Sprintf(`
<html>
<body style="font-family: Arial, sans-serif;">
<h2 style="color: #d32f2f;">%s incident reopened (relapse #%d)</h2>
<p>The incident for <strong>%s</strong> has been reopened after a brief recovery.</p>
<ul>
<li><strong>Check:</strong> %s</li>
<li><strong>Type:</strong> %s</li>
<li><strong>Started at:</strong> %s</li>
<li><strong>Relapse count:</strong> %d</li>
<li><strong>Failure count:</strong> %d</li>
<li><strong>Incident ID:</strong> %s</li>
</ul>
<p style="color: #666;">This is an automated notification from SolidPing.</p>
</body>
</html>
`, checkName, payload.Incident.RelapseCount, checkName, checkName, payload.Check.Type,
		payload.Incident.StartedAt.Format("2006-01-02 15:04:05"),
		payload.Incident.RelapseCount, payload.Incident.FailureCount, payload.Incident.UID)
}

func (s *EmailSender) buildIncidentReopenedText(checkName string, payload *Payload) string {
	return fmt.Sprintf(`%s incident reopened (relapse #%d)

The incident for %s has been reopened after a brief recovery.

Check: %s
Type: %s
Started at: %s
Relapse count: %d
Failure count: %d
Incident ID: %s

This is an automated notification from SolidPing.
`, checkName, payload.Incident.RelapseCount, checkName, checkName, payload.Check.Type,
		payload.Incident.StartedAt.Format("2006-01-02 15:04:05"),
		payload.Incident.RelapseCount, payload.Incident.FailureCount, payload.Incident.UID)
}
