package usernotifications

import (
	"context"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	slackclient "github.com/fclairamb/solidping/server/internal/integrations/slack"
)

// EmailSenderAdapter wraps the core email.Sender to implement EmailSender.
type EmailSenderAdapter struct {
	inner email.Sender
}

// NewEmailSenderAdapter builds an adapter.
func NewEmailSenderAdapter(s email.Sender) *EmailSenderAdapter {
	return &EmailSenderAdapter{inner: s}
}

// SendTestEmail sends a plain "Test notification" email.
func (a *EmailSenderAdapter) SendTestEmail(ctx context.Context, to string) error {
	if a == nil || a.inner == nil {
		return ErrEmailSenderNotConfigured
	}

	msg := &email.Message{
		Recipients: email.Recipients{To: []string{to}},
		Subject:    "Test notification from SolidPing",
		Text:       "This is a test notification from SolidPing. Your email delivery is working correctly.",
	}

	if _, err := a.inner.Send(ctx, msg); err != nil {
		return fmt.Errorf("send test email: %w", err)
	}

	return nil
}

// SlackDMSenderAdapter wraps the Slack access-token to send a DM.
type SlackDMSenderAdapter struct{}

// NewSlackDMSenderAdapter builds an adapter.
func NewSlackDMSenderAdapter() *SlackDMSenderAdapter {
	return &SlackDMSenderAdapter{}
}

// SendDMTest sends a test DM to slackUserID using the org's Slack bot token.
func (a *SlackDMSenderAdapter) SendDMTest(
	ctx context.Context, ch *models.Channel, slackUserID string,
) error {
	settings, err := models.SlackSettingsFromJSONMap(ch.Settings)
	if err != nil {
		return fmt.Errorf("parse slack settings: %w", err)
	}

	if settings.AccessToken == "" {
		return ErrSlackClientNotConfigured
	}

	client := slackclient.NewClient(settings.AccessToken)

	msg := &slackclient.MessageResponse{
		Text: "Test notification from SolidPing — your Slack DM delivery is working correctly.",
	}

	_, err = client.PostMessage(ctx, slackclient.PostMessageOptions{
		Channel: slackUserID,
		Message: msg,
	})
	if err != nil {
		return fmt.Errorf("send slack DM test: %w", err)
	}

	return nil
}
