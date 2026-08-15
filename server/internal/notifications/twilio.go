package notifications

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	smssvc "github.com/fclairamb/solidping/server/internal/integrations/sms"
	"github.com/fclairamb/solidping/server/internal/integrations/twilio"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

var (
	// ErrTwilioNoRecipients is returned when a Twilio connection has no shared
	// to_numbers configured for a direct-channel (registry-path) send.
	ErrTwilioNoRecipients = errors.New("twilio connection has no to_numbers configured")
	// ErrTwilioNotConfigured is returned when required Twilio credentials are
	// missing from the connection settings.
	ErrTwilioNotConfigured = errors.New("twilio connection is not fully configured")
)

// smsAckTokenTTL bounds how long the ack link embedded in an SMS stays valid.
// Mirrors the email ack TTL (7 days from incident start).
const smsAckTokenTTL = 7 * 24 * time.Hour

// TwilioSender sends SMS notifications via Twilio's REST API. It is the
// registry-path (direct-channel) sender: it fans out to the shared to_numbers
// list on the connection. Per-user phone paging goes through the escalation
// dispatcher, not this sender.
type TwilioSender struct {
	// BaseURL overrides the Twilio API base for tests. Empty = the real API.
	BaseURL string
}

// resolveBaseURL decides which Twilio host a send goes to. Precedence: the
// test override (BaseURL) wins when set — it exists only so tests can point
// the sender at an httptest fake, and a test that sets it always wants that
// exact host regardless of the connection's region. In real operation
// BaseURL is always empty, so the connection's region decides:
// twilio.BaseURLForRegion("") still resolves to twilio.DefaultBaseURL, so a
// connection with no region behaves exactly as before this field existed.
func (s *TwilioSender) resolveBaseURL(region string) string {
	if s.BaseURL != "" {
		return s.BaseURL
	}

	return twilio.BaseURLForRegion(region)
}

// Send delivers an SMS to every configured shared recipient.
func (s *TwilioSender) Send(ctx context.Context, jctx *jobdef.JobContext, payload *Payload) error {
	settings, err := models.TwilioSettingsFromJSONMap(payload.Integration.Settings)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrTwilioNotConfigured, err)
	}

	if settings.AccountSID == "" || settings.AuthToken == "" {
		return ErrTwilioNotConfigured
	}

	if settings.FromNumber == "" && settings.MessagingServiceSID == "" {
		return ErrTwilioNotConfigured
	}

	if len(settings.ToNumbers) == 0 {
		return ErrTwilioNoRecipients
	}

	body := s.buildBody(jctx, payload, settings)

	// Goes through the provider-neutral seam like every other send path. This
	// one is always bring-your-own by construction — a channel target IS a
	// per-org connection — but there is no reason for it to be the last place
	// still building a concrete provider client by hand.
	sender := smssvc.NewTwilioSender(&smssvc.TwilioCredentials{
		AccountSID:          settings.AccountSID,
		AuthToken:           settings.AuthToken,
		Region:              settings.Region,
		BaseURL:             s.BaseURL,
		From:                settings.FromNumber,
		MessagingServiceSID: settings.MessagingServiceSID,
	})

	var statusCallback string
	if payload.AppBaseURL != "" {
		statusCallback = strings.TrimRight(payload.AppBaseURL, "/") +
			"/api/v1/integrations/twilio/status?cid=" + payload.Integration.UID
	}

	var lastSID string
	for _, toNumber := range settings.ToNumbers {
		res, sendErr := sender.SendSMS(ctx, &smssvc.SendParams{
			To:             toNumber,
			Body:           body,
			StatusCallback: statusCallback,
		})
		if sendErr != nil {
			return fmt.Errorf("sending SMS to %s: %w", toNumber, sendErr)
		}

		if res != nil {
			lastSID = res.ProviderMessageID
		}
	}

	// Surface the last message SID on the audit row (there may be several — the
	// audit model carries a single message_id, so the last one wins).
	payload.MessageID = lastSID

	return nil
}

// buildBody renders a short (GSM-7-friendly) SMS body for the event, appending
// a signed one-click ack link for down/escalated events.
func (s *TwilioSender) buildBody(
	jctx *jobdef.JobContext, payload *Payload, settings *models.TwilioSettings,
) string {
	checkName := getCheckName(payload.Check)
	org := payload.OrgSlug

	var msg string
	switch payload.EventType {
	case eventTypeIncidentResolved:
		return fmt.Sprintf("[%s] %s: %s RECOVERED.", productName, org, checkName) + twilio.OptOutFooter
	case eventTypeIncidentEscalated:
		msg = fmt.Sprintf("[%s] %s: %s STILL DOWN (escalated).", productName, org, checkName)
	case eventTypeIncidentReopened:
		msg = fmt.Sprintf("[%s] %s: %s DOWN again.", productName, org, checkName)
	default: // created
		msg = fmt.Sprintf("[%s] %s: %s is DOWN.", productName, org, checkName)
	}

	if ackURL := s.ackURL(jctx, payload, settings); ackURL != "" {
		msg += " Ack: " + ackURL
	}

	return msg + twilio.OptOutFooter
}

// ackURL builds the signed magic-link ack URL for the SMS. The shared
// to_number is used as the recipient identity embedded in the token (the
// direct-channel send has no per-user recipient). Returns "" when the event
// can't be acked or the JWT secret / base URL is unavailable.
func (s *TwilioSender) ackURL(
	jctx *jobdef.JobContext, payload *Payload, settings *models.TwilioSettings,
) string {
	if !canAckEvent(payload.EventType) {
		return ""
	}

	if jctx == nil || jctx.AppConfig == nil {
		return ""
	}

	recipient := ""
	if len(settings.ToNumbers) > 0 {
		recipient = settings.ToNumbers[0]
	}

	secret := []byte(jctx.AppConfig.Auth.JWTSecret)
	exp := payload.Incident.StartedAt.Add(smsAckTokenTTL)

	return buildAckURL(payload.AppBaseURL, payload.OrgSlug, payload.Incident.UID, recipient, secret, exp)
}
