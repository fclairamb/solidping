// Package sms is the provider-neutral seam for outbound SMS and voice.
//
// Before this package existed, *models.TwilioSettings was threaded as a
// concrete type all the way down the send path, so there was no point at which
// a second provider could be introduced. Everything downstream now depends on
// the Sender / VoiceCaller interfaces here instead, and the choice of provider
// — and of *whose* credentials pay for the message — is made once, by the
// Resolver, at the top of the send.
package sms

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/ovhsms"
	"github.com/fclairamb/solidping/server/internal/integrations/twilio"
)

// ErrNoSMSProvider is returned when neither a per-org integration nor the
// instance configuration can send an SMS.
var ErrNoSMSProvider = errors.New("no SMS provider available")

// ErrNoVoiceProvider is returned when neither a per-org integration nor the
// instance configuration can place a call.
var ErrNoVoiceProvider = errors.New("no voice provider available")

// ErrUnknownProvider is returned for an SP_SMS_PROVIDER value that is neither
// "twilio" nor "ovh".
var ErrUnknownProvider = errors.New("unknown SMS provider")

// SendParams are the provider-neutral inputs to one SMS send.
//
// From deliberately does NOT appear here: it is not the same concept across
// providers (Twilio wants an E.164 number or a MessagingServiceSID; OVH wants
// a pre-registered alphanumeric sender or a short number). The sender identity
// is baked into each provider's constructor instead.
type SendParams struct {
	// To is an E.164 destination, validated by twilio.ValidE164 upstream.
	To string
	// Body is the message text.
	Body string
	// StatusCallback is the delivery-receipt URL to request, empty for none.
	// Honored by providers that accept a per-message callback (Twilio); OVH
	// carries no per-job callback field, so its adapter ignores this and the
	// receipts arrive on the service-level callback instead.
	StatusCallback string
}

// SendResult identifies the accepted message at the provider.
type SendResult struct {
	// ProviderMessageID is the Twilio message SID or the OVH job id. It is the
	// key delivery receipts are matched on, so it must be the same string the
	// provider will quote back.
	ProviderMessageID string
}

// Sender sends one SMS. The single method is the whole seam: tests use a fake
// Sender rather than swapping a package-level constructor var.
type Sender interface {
	SendSMS(ctx context.Context, p *SendParams) (*SendResult, error)
}

// CallParams are the provider-neutral inputs to one outbound voice call.
type CallParams struct {
	To             string
	TwiMLURL       string
	StatusCallback string
}

// VoiceCaller places one outbound voice call. Twilio-only in practice — OVH
// has no voice API — but expressed as an interface so the escalation path
// depends on no concrete provider and tests can fake it.
type VoiceCaller interface {
	PlaceCall(ctx context.Context, p *CallParams) (*SendResult, error)
}

// PhoneSender is a provider that covers BOTH phone capabilities. One Twilio
// account does, which is why a bring-your-own integration can serve as the
// org's SMS and voice provider at once.
type PhoneSender interface {
	Sender
	VoiceCaller
}

// twilioSender adapts the existing Twilio client to Sender / VoiceCaller. It
// is constructible from EITHER credential source — instance config or a
// per-org integration's decrypted settings — because both modes share this one
// send path.
type twilioSender struct {
	client              *twilio.Client
	from                string
	messagingServiceSID string
	voiceFrom           string
}

// TwilioCredentials is the credential set a Twilio-backed sender needs,
// flattened so the instance config and a per-org integration produce the same
// shape.
type TwilioCredentials struct {
	AccountSID          string
	AuthToken           string
	Region              string
	BaseURL             string
	From                string
	MessagingServiceSID string
	VoiceFrom           string
}

// TwilioBaseURL returns the API base a Twilio-backed sender will target: the
// explicit BaseURL override when set, else the host derived from the account's
// region. Exported because "does this account's region actually reach the
// matching regional host?" is worth asserting directly — a silent fallback to
// the default US1 edge makes valid credentials look invalid.
func TwilioBaseURL(creds *TwilioCredentials) string {
	if creds.BaseURL != "" {
		return creds.BaseURL
	}

	return twilio.BaseURLForRegion(creds.Region)
}

// NewTwilioSender builds a Twilio-backed PhoneSender. BaseURL overrides the
// region-derived host when set (test/E2E fakes, egress proxies).
func NewTwilioSender(creds *TwilioCredentials) PhoneSender {
	base := TwilioBaseURL(creds)

	return &twilioSender{
		client:              twilio.NewClientWithBaseURL(creds.AccountSID, creds.AuthToken, base),
		from:                creds.From,
		messagingServiceSID: creds.MessagingServiceSID,
		voiceFrom:           creds.VoiceFrom,
	}
}

func (s *twilioSender) SendSMS(ctx context.Context, params *SendParams) (*SendResult, error) {
	res, err := s.client.SendSMS(ctx, &twilio.SendSMSParams{
		To:                  params.To,
		From:                s.from,
		MessagingServiceSID: s.messagingServiceSID,
		Body:                params.Body,
		StatusCallback:      params.StatusCallback,
	})
	if err != nil {
		return nil, fmt.Errorf("twilio send: %w", err)
	}

	return &SendResult{ProviderMessageID: res.SID}, nil
}

func (s *twilioSender) PlaceCall(ctx context.Context, params *CallParams) (*SendResult, error) {
	res, err := s.client.CreateCall(ctx, twilio.CreateCallParams{
		To:             params.To,
		From:           s.voiceFrom,
		TwiMLURL:       params.TwiMLURL,
		StatusCallback: params.StatusCallback,
	})
	if err != nil {
		return nil, fmt.Errorf("twilio call: %w", err)
	}

	return &SendResult{ProviderMessageID: res.SID}, nil
}

// ovhSender adapts the OVH client to Sender. There is no VoiceCaller here:
// OVH has no voice API, which is exactly why SMS and voice resolve
// independently.
type ovhSender struct {
	client *ovhsms.Client
	sender string
}

func (s *ovhSender) SendSMS(ctx context.Context, params *SendParams) (*SendResult, error) {
	// params.StatusCallback is deliberately ignored: OVH's POST /sms/{svc}/jobs
	// carries no per-job callback field. Delivery receipts arrive on the
	// service-level `callBack` URL, which the OVH DLR handler authenticates
	// with its own token.
	report, err := s.client.Send(ctx, &ovhsms.SendParams{
		Receivers: []string{params.To},
		Message:   params.Body,
		Sender:    s.sender,
	})
	if err != nil {
		return nil, fmt.Errorf("ovh send: %w", err)
	}

	id := ""
	if len(report.IDs) > 0 {
		id = strconv.FormatInt(report.IDs[0], 10)
	}

	return &SendResult{ProviderMessageID: id}, nil
}

// NewInstanceSender builds the shared, instance-level Sender from SMSConfig.
// Built ONCE at startup and injected, so no send path constructs a client from
// a package-level var. Returns (nil, nil) when the instance is not configured
// to send — an inactive config is not an error, it is the default.
func NewInstanceSender(ctx context.Context, cfg *config.SMSConfig) (Sender, error) {
	if !cfg.Active() {
		return nil, nil //nolint:nilnil // "not configured" is the documented default, not a failure.
	}

	switch cfg.ResolvedProvider() {
	case config.SMSProviderTwilio:
		if !twilio.ValidRegion(cfg.Twilio.Region) {
			return nil, fmt.Errorf("%w: invalid SP_SMS_TWILIO_REGION %q", ErrUnknownProvider, cfg.Twilio.Region)
		}

		return NewTwilioSender(&TwilioCredentials{
			AccountSID:          cfg.Twilio.AccountSID,
			AuthToken:           cfg.Twilio.AuthToken,
			Region:              cfg.Twilio.Region,
			BaseURL:             cfg.Twilio.BaseURL,
			From:                cfg.Sender,
			MessagingServiceSID: cfg.Twilio.MessagingServiceSID,
		}), nil
	case config.SMSProviderOVH:
		client, err := ovhsms.NewClient(ctx, &ovhsms.Config{
			Endpoint:          cfg.OVH.Endpoint,
			BaseURL:           cfg.OVH.BaseURL,
			ApplicationKey:    cfg.OVH.ApplicationKey,
			ApplicationSecret: cfg.OVH.ApplicationSecret,
			ConsumerKey:       cfg.OVH.ConsumerKey,
			ServiceName:       cfg.OVH.ServiceName,
		})
		if err != nil {
			return nil, fmt.Errorf("build ovh sms client: %w", err)
		}

		return &ovhSender{client: client, sender: cfg.Sender}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, cfg.ResolvedProvider())
	}
}

// NewInstanceVoiceCaller builds the shared, instance-level VoiceCaller from
// VoiceConfig. Returns (nil, nil) when voice is not configured.
func NewInstanceVoiceCaller(cfg *config.VoiceConfig) (VoiceCaller, error) {
	if !cfg.Active() {
		return nil, nil //nolint:nilnil // "not configured" is the documented default, not a failure.
	}

	if !twilio.ValidRegion(cfg.Twilio.Region) {
		return nil, fmt.Errorf("%w: invalid SP_VOICE_TWILIO_REGION %q", ErrUnknownProvider, cfg.Twilio.Region)
	}

	return NewTwilioSender(&TwilioCredentials{
		AccountSID: cfg.Twilio.AccountSID,
		AuthToken:  cfg.Twilio.AuthToken,
		Region:     cfg.Twilio.Region,
		BaseURL:    cfg.Twilio.BaseURL,
		VoiceFrom:  cfg.FromNumber,
	}), nil
}

// NewOrgTwilioSender builds the bring-your-own sender from a per-org Twilio
// integration's decrypted settings. One Twilio account covers both
// capabilities, so the same object serves as Sender and VoiceCaller.
func NewOrgTwilioSender(settings *models.TwilioSettings) PhoneSender {
	return NewTwilioSender(&TwilioCredentials{
		AccountSID:          settings.AccountSID,
		AuthToken:           settings.AuthToken,
		Region:              settings.Region,
		From:                settings.FromNumber,
		MessagingServiceSID: settings.MessagingServiceSID,
		VoiceFrom:           settings.VoiceFromNumber,
	})
}

// ConfigureOVHDeliveryCallback points the OVH SMS account's service-level
// `callBack` at callbackURL, reusing the already-constructed client so the
// /auth/time delta is not fetched twice.
//
// A no-op (nil) unless the instance sender is actually OVH-backed and a
// callback URL is available. Failures are returned rather than swallowed, but
// callers should treat them as a warning: an operator can set the same URL in
// the OVH control panel, and losing delivery receipts must not stop the
// process from booting and paging people.
func ConfigureOVHDeliveryCallback(ctx context.Context, sender Sender, callbackURL string) error {
	ovh, ok := sender.(*ovhSender)
	if !ok || callbackURL == "" {
		return nil
	}

	if err := ovh.client.SetDeliveryCallback(ctx, callbackURL); err != nil {
		return fmt.Errorf("register ovh delivery callback: %w", err)
	}

	return nil
}
