package notifications

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	smssvc "github.com/fclairamb/solidping/server/internal/integrations/sms"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

var (
	// ErrSMSFactorNoRecipients is returned when an SMS Factor connection has
	// no shared to_numbers configured for a direct-channel send.
	ErrSMSFactorNoRecipients = errors.New("smsfactor connection has no to_numbers configured")
	// ErrSMSFactorNotConfigured is returned when the API token is missing from
	// the connection settings.
	ErrSMSFactorNotConfigured = newConfigError("smsfactor connection is not fully configured", "SMS Factor API token")
)

// SMSFactorSender sends SMS notifications via the SMS Factor API. Like
// TwilioSender it is the registry-path (direct-channel) sender and fans out
// to the shared to_numbers list; per-user phone paging goes through the
// escalation dispatcher. The bodies are the shared SMS bodies without the
// Twilio opt-out footer, which is a US carrier requirement.
type SMSFactorSender struct {
	// BaseURL overrides the SMS Factor API base for tests. Empty = the real API.
	BaseURL string
}

// Send delivers an SMS to every configured shared recipient, one request per
// recipient so a filtered number cannot hide the others' outcome.
func (s *SMSFactorSender) Send(ctx context.Context, jctx *jobdef.JobContext, payload *Payload) error {
	settings, err := models.SMSFactorSettingsFromJSONMap(payload.Integration.Settings)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSMSFactorNotConfigured, err)
	}

	if strings.TrimSpace(settings.Token) == "" {
		return ErrSMSFactorNotConfigured
	}

	if len(settings.ToNumbers) == 0 {
		return ErrSMSFactorNoRecipients
	}

	body := buildSMSBody(jctx, payload, settings.ToNumbers, "")

	sender := smssvc.NewSMSFactorSender(settings.Token, s.BaseURL, settings.Sender)

	var lastTicket string

	for _, toNumber := range settings.ToNumbers {
		res, sendErr := sender.SendSMS(ctx, &smssvc.SendParams{To: toNumber, Body: body})
		if sendErr != nil {
			return fmt.Errorf("sending SMS to %s: %w", toNumber, sendErr)
		}

		if res != nil {
			lastTicket = res.ProviderMessageID
		}
	}

	// The audit model carries a single message_id, so the last ticket wins.
	payload.MessageID = lastTicket

	return nil
}
