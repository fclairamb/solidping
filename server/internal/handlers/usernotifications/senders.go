package usernotifications

import (
	"context"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/email"
	slackclient "github.com/fclairamb/solidping/server/internal/integrations/slack"
)

// EmailSenderAdapter wraps the core email.Sender to implement EmailSender.
type EmailSenderAdapter struct {
	inner     email.Sender
	formatter email.Formatter
}

// NewEmailSenderAdapter builds an adapter.
func NewEmailSenderAdapter(s email.Sender, formatter email.Formatter) *EmailSenderAdapter {
	return &EmailSenderAdapter{inner: s, formatter: formatter}
}

// SendTestEmail sends a "Test notification" email, rendered through the
// shared formatter (test-email.html) so it gets an HTML part alongside the
// plaintext one instead of shipping text-only.
func (a *EmailSenderAdapter) SendTestEmail(ctx context.Context, recipient string) error {
	if a == nil || a.inner == nil {
		return ErrEmailSenderNotConfigured
	}

	if a.formatter == nil {
		return ErrEmailFormatterNotConfigured
	}

	subject, htmlBody, textBody, err := a.formatter.Format("test-email.html", map[string]any{
		"Subject": "Test notification from SolidPing",
		"Heading": "Test notification from SolidPing",
		"Body":    "This is a test notification from SolidPing. Your email delivery is working correctly.",
	})
	if err != nil {
		return fmt.Errorf("format test email: %w", err)
	}

	msg := &email.Message{
		Recipients:       email.Recipients{To: []string{recipient}},
		Subject:          subject,
		HTML:             htmlBody,
		Text:             textBody,
		SupportReplyable: email.SupportReplyable("test-email.html"),
	}

	if _, err := a.inner.Send(ctx, msg); err != nil {
		return fmt.Errorf("send test email: %w", err)
	}

	return nil
}

// SlackDMSenderAdapter posts a test DM with an already-resolved bot token.
//
// It deliberately does NOT parse the integration row itself: the bot token
// lives in the encrypted `settings_private` envelope, and a reader that only
// looks at the public settings silently sees an empty token (spec
// 2026-09-18-02). Resolving it is the service's job, through the one shared
// helper.
type SlackDMSenderAdapter struct{}

// NewSlackDMSenderAdapter builds an adapter.
func NewSlackDMSenderAdapter() *SlackDMSenderAdapter {
	return &SlackDMSenderAdapter{}
}

// SendDMTest sends a test DM to slackUserID using the org's Slack bot token.
func (a *SlackDMSenderAdapter) SendDMTest(
	ctx context.Context, accessToken, slackUserID string,
) error {
	if accessToken == "" {
		return ErrSlackClientNotConfigured
	}

	client := slackclient.NewClient(accessToken)

	msg := &slackclient.MessageResponse{
		Text: "Test notification from SolidPing — your Slack DM delivery is working correctly.",
	}

	if _, err := client.PostMessage(ctx, slackclient.PostMessageOptions{
		Channel: slackUserID,
		Message: msg,
	}); err != nil {
		return fmt.Errorf("send slack DM test: %w", err)
	}

	return nil
}
