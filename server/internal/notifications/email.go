package notifications

import (
	"context"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

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
	recipientList, ok := payload.Connection.Settings["recipients"].([]any)
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

	content := s.buildEmailContent(payload)

	// Add subject prefix if configured
	if prefix, ok := payload.Connection.Settings["subject_prefix"].(string); ok && prefix != "" {
		content.subject = prefix + " " + content.subject
	}

	// Build and send email message
	msg := &email.Message{
		Recipients: email.Recipients{To: emailAddresses},
		Subject:    content.subject,
		HTML:       content.htmlBody,
		Text:       content.textBody,
	}

	_, err := jctx.Services.EmailSender.Send(ctx, msg)
	return err
}

type emailContent struct {
	subject  string
	htmlBody string
	textBody string
}

func (s *EmailSender) buildEmailContent(payload *Payload) emailContent {
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
			s.buildIncidentCreatedHTML(checkName, payload),
			s.buildIncidentCreatedText(checkName, payload),
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
			s.buildIncidentEscalatedHTML(checkName, payload),
			s.buildIncidentEscalatedText(checkName, payload),
		}
	case eventTypeIncidentReopened:
		return emailContent{
			fmt.Sprintf("[REOPENED] %s incident reopened (relapse #%d)", checkName, payload.Incident.RelapseCount),
			s.buildIncidentReopenedHTML(checkName, payload),
			s.buildIncidentReopenedText(checkName, payload),
		}
	default:
		return emailContent{
			fmt.Sprintf("[SolidPing] %s incident update", checkName),
			fmt.Sprintf("<p>Incident update for <strong>%s</strong>: %s</p>", checkName, payload.EventType),
			fmt.Sprintf("Incident update for %s: %s", checkName, payload.EventType),
		}
	}
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
