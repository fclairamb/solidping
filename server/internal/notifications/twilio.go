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
	ErrTwilioNotConfigured = newConfigError("twilio connection is not fully configured", "Twilio credentials")
)

// smsAckTokenTTL bounds how long the ack link embedded in an SMS stays valid.
// Mirrors the email ack TTL (7 days from incident start).
const smsAckTokenTTL = 7 * 24 * time.Hour

// TwilioSender sends SMS notifications via Twilio's REST API. It is the
// registry-path (direct-channel) sender: it fans out to the shared to_numbers
// list on the connection. Per-user phone paging goes through the escalation
// dispatcher, not this sender.
type TwilioSender struct {
	// BaseURL overrides the Twilio API base for tests. Empty = the real API,
	// in which case the connection's region decides the host (see
	// sms.TwilioBaseURL, which owns that precedence for every send path).
	BaseURL string
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

// buildBody renders the Twilio SMS body: the shared SMS body with the US
// opt-out footer every recurring Twilio message must carry.
func (s *TwilioSender) buildBody(
	jctx *jobdef.JobContext, payload *Payload, settings *models.TwilioSettings,
) string {
	var toNumbers []string
	if settings != nil {
		toNumbers = settings.ToNumbers
	}

	return buildSMSBody(jctx, payload, toNumbers, twilio.OptOutFooter)
}

// buildSMSBody renders a short (GSM-7-friendly) SMS body for the event,
// appending a signed one-click ack link for down/escalated events and then
// footer (Twilio's opt-out disclosure, empty for providers that need none).
// toNumbers are the connection's shared recipients; the first one is the
// identity embedded in the ack token. Shared by every SMS channel sender.
func buildSMSBody(jctx *jobdef.JobContext, payload *Payload, toNumbers []string, footer string) string {
	if isDegradedIncident(payload.Incident) && !closedWithoutRecovery(payload.Incident) {
		return buildDegradedSMSBody(jctx, payload, toNumbers, footer)
	}

	checkName := getCheckName(payload.Check)
	org := payload.OrgSlug

	var msg string
	switch payload.EventType {
	case eventTypeIncidentResolved:
		if ResolvedByCheckDeletion(payload.Incident) {
			return fmt.Sprintf("[%s] %s: %s was deleted, incident closed.", productName, org, checkName) + footer
		}

		if ResolvedByCheckDisable(payload.Incident) {
			return fmt.Sprintf("[%s] %s: %s was disabled, incident closed.", productName, org, checkName) + footer
		}

		return fmt.Sprintf("[%s] %s: %s RECOVERED.", productName, org, checkName) +
			smsRegionSentence(payload) + footer
	case eventTypeIncidentEscalated:
		msg = fmt.Sprintf("[%s] %s: %s STILL DOWN (escalated).", productName, org, checkName)
	case eventTypeIncidentReopened:
		msg = fmt.Sprintf("[%s] %s: %s DOWN again.", productName, org, checkName)
	case eventTypeIncidentReminder:
		// Not fanned out to SMS channels by default (NotifiesReminders is
		// false): kept so a reminder never renders as a fresh "is DOWN".
		msg = fmt.Sprintf("[%s] %s: %s STILL DOWN, not acknowledged.", productName, org, checkName)
	default: // created
		msg = fmt.Sprintf("[%s] %s: %s is DOWN.", productName, org, checkName)
	}

	msg += smsRegionSentence(payload)

	if ackURL := smsAckURL(jctx, payload, toNumbers); ackURL != "" {
		msg += " Ack: " + ackURL
	}

	return msg + footer
}

// buildDegradedSMSBody is buildSMSBody for a degraded incident: it was never
// down, so the SMS says DEGRADED and gives the pattern, and its end is "steady
// again" rather than RECOVERED. No region sentence: a degraded incident has
// none.
func buildDegradedSMSBody(jctx *jobdef.JobContext, payload *Payload, toNumbers []string, footer string) string {
	checkName := getCheckName(payload.Check)
	prefix := fmt.Sprintf("[%s] %s: ", productName, payload.OrgSlug)
	cause := degradedCause(payload.Incident)

	var msg string

	switch payload.EventType {
	case eventTypeIncidentResolved:
		if degradedTurnedOff(payload.Incident) {
			return prefix + checkName + ": degraded detection turned off, episode closed." + footer
		}

		return prefix + checkName + " is steady again." + footer
	case eventTypeIncidentEscalated:
		msg = prefix + checkName + " STILL DEGRADED (escalated): " + cause + "."
	case eventTypeIncidentReopened:
		msg = prefix + checkName + " DEGRADED again: " + cause + "."
	default: // created
		msg = prefix + checkName + " is DEGRADED: " + cause + "."
	}

	if ackURL := smsAckURL(jctx, payload, toNumbers); ackURL != "" {
		msg += " Ack: " + ackURL
	}

	return msg + footer
}

// smsRegionSentence is the region line for an SMS: the plain region names,
// never the emoji (one emoji forces UCS-2 and halves the SMS length). Empty
// when the payload shows no region line.
func smsRegionSentence(payload *Payload) string {
	title, text := RegionLabelPlainFor(payload)
	if text == "" {
		return ""
	}

	return " " + title + ": " + text + "."
}

// smsAckURL builds the signed magic-link ack URL for the SMS. The first shared
// to_number is used as the recipient identity embedded in the token (the
// direct-channel send has no per-user recipient). Returns "" when the event
// can't be acked or the JWT secret / base URL is unavailable.
func smsAckURL(jctx *jobdef.JobContext, payload *Payload, toNumbers []string) string {
	if !canAckEvent(payload.EventType) {
		return ""
	}

	if jctx == nil || jctx.AppConfig == nil {
		return ""
	}

	recipient := ""
	if len(toNumbers) > 0 {
		recipient = toNumbers[0]
	}

	secret := []byte(jctx.AppConfig.Auth.JWTSecret)
	exp := payload.Incident.StartedAt.Add(smsAckTokenTTL)

	return buildAckURL(payload.AppBaseURL, payload.OrgSlug, payload.Incident.UID, recipient, secret, exp)
}
