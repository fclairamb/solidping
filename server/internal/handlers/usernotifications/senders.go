package usernotifications

import (
	"context"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
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
// plaintext one instead of shipping text-only. The route being tested is the
// acting user's own, so the mail speaks their language (the user the auth
// middleware put on ctx; English/UTC without one).
func (a *EmailSenderAdapter) SendTestEmail(ctx context.Context, address string) error {
	if a == nil || a.inner == nil {
		return ErrEmailSenderNotConfigured
	}

	if a.formatter == nil {
		return ErrEmailFormatterNotConfigured
	}

	actor, _ := ctx.Value(base.ContextKeyUser).(*models.User)
	loc := recipient.ForUser(actor)

	subject, htmlBody, textBody, err := a.formatter.Format("test-email.html", loc, TestEmailData(loc))
	if err != nil {
		return fmt.Errorf("format test email: %w", err)
	}

	msg := &email.Message{
		Recipients:       email.Recipients{To: []string{address}},
		Subject:          subject,
		HTML:             htmlBody,
		Text:             textBody,
		SupportReplyable: email.SupportReplyable("test-email.html"),
		Locale:           loc.Lang(),
		Timezone:         loc.Timezone(),
	}

	if _, err := a.inner.Send(ctx, msg); err != nil {
		return fmt.Errorf("send test email: %w", err)
	}

	return nil
}

// TestEmailData is the test-email.html view model of a notification route
// test, in loc.
func TestEmailData(loc i18n.Locale) map[string]any {
	title := loc.T("alerts.testEmail.route.title")

	return map[string]any{
		"Subject": title,
		"Heading": title,
		"Body":    loc.T("alerts.testEmail.route.body"),
	}
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
