package sms

import (
	"context"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/twilioconn"
)

// Mode says whose credentials — and therefore whose bill — a send goes on.
// The distinction is not cosmetic: the instance-spend guards (the
// instance-wide hourly cap and the destination-country allow-list) apply to
// ModeServer only.
type Mode string

// Modes.
const (
	// ModeServer means the send uses this instance's own SP_SMS_*/SP_VOICE_*
	// credentials. This is the default and needs no per-org setup.
	ModeServer Mode = "server"
	// ModeBringYourOwn means the org has its own Twilio integration (SMS and
	// voice) or SMS Factor integration (SMS only), which overrides the
	// instance provider for that capability. The customer's account is billed.
	ModeBringYourOwn Mode = "bring_your_own"
	// ModeUnavailable means neither is configured — the channel simply cannot
	// send for this org.
	ModeUnavailable Mode = "unavailable"
)

// Resolution is the outcome of picking a provider for one org.
type Resolution struct {
	// SMSMode / VoiceMode are resolved INDEPENDENTLY, per capability. An
	// instance can run OVH for SMS and Twilio for voice at the same time, and
	// an org whose own integration configures no voice_from_number still gets
	// the instance's voice.
	SMSMode   Mode
	VoiceMode Mode
	// Sender is nil when SMSMode is ModeUnavailable.
	Sender Sender
	// Voice is nil when VoiceMode is ModeUnavailable.
	Voice VoiceCaller
	// Conn is the org's own Twilio integration when one exists (whatever mode
	// each capability ended up in), nil otherwise. Callbacks need its UID.
	// Never set for an SMS Factor bring-your-own send: that provider has no
	// Twilio-style callbacks, so a nil Conn with SMSMode == ModeBringYourOwn
	// means "no status callback".
	Conn *models.Integration
	// SenderID is the sender identity SMS goes out under — informational, for
	// the UI panel. Never a credential.
	SenderID string
}

// SMSAvailable reports whether an SMS can be sent for this org.
func (r *Resolution) SMSAvailable() bool { return r.Sender != nil }

// VoiceAvailable reports whether a call can be placed for this org.
func (r *Resolution) VoiceAvailable() bool { return r.Voice != nil }

// InstanceCredentialsForSMS reports whether the SMS goes out on the SERVER's
// credentials, i.e. whether the instance-spend guards apply. Callers use this
// rather than comparing modes, so the scoping rule lives in one place.
func (r *Resolution) InstanceCredentialsForSMS() bool { return r.SMSMode == ModeServer }

// InstanceCredentialsForVoice is InstanceCredentialsForSMS for calls. Voice is
// resolved on its own: an org whose integration has no voice_from_number
// places calls on the instance's credentials even though its SMS are its own,
// and those calls consume the plan's monthly cap.
func (r *Resolution) InstanceCredentialsForVoice() bool { return r.VoiceMode == ModeServer }

// Resolver picks, per org and per capability, between the org's own Twilio
// integration (or, for SMS only, its SMS Factor integration) and the
// instance-level provider.
type Resolver struct {
	db    db.Service
	creds credentials.Service

	// instanceSender / instanceVoice are built ONCE at startup from
	// SMSConfig / VoiceConfig and shared by every org that has not brought its
	// own account. Nil when the instance is not configured for that capability.
	instanceSender Sender
	instanceVoice  VoiceCaller
	// instanceSenderID is the instance sender identity, for the UI panel.
	instanceSenderID string
	// orgSenderFactory builds the bring-your-own sender from a per-org Twilio
	// integration's decrypted settings. Injected per resolver rather than being
	// a package-level var, so parallel tests can never race on it; nil means
	// NewOrgTwilioSender.
	orgSenderFactory func(*models.TwilioSettings) PhoneSender
	// orgSMSFactorFactory is orgSenderFactory for a per-org SMS Factor
	// integration; nil means NewOrgSMSFactorSender.
	orgSMSFactorFactory func(*models.SMSFactorSettings) Sender
}

// ResolverOption customizes a Resolver at construction.
type ResolverOption func(*Resolver)

// WithOrgSenderFactory overrides how a bring-your-own sender is built from a
// per-org integration's settings. Exists for tests, which point it at a fake
// provider instead of the real Twilio host.
func WithOrgSenderFactory(factory func(*models.TwilioSettings) PhoneSender) ResolverOption {
	return func(r *Resolver) { r.orgSenderFactory = factory }
}

// WithOrgSMSFactorSenderFactory overrides how a bring-your-own SMS Factor
// sender is built from a per-org integration's settings (tests).
func WithOrgSMSFactorSenderFactory(factory func(*models.SMSFactorSettings) Sender) ResolverOption {
	return func(r *Resolver) { r.orgSMSFactorFactory = factory }
}

// NewOrgSMSFactorSender builds the bring-your-own SMS sender from a per-org
// SMS Factor integration's decrypted settings.
func NewOrgSMSFactorSender(settings *models.SMSFactorSettings) Sender {
	return NewSMSFactorSender(settings.Token, "", settings.Sender)
}

// NewResolver builds a resolver. instanceSender / instanceVoice may be nil,
// which simply means the instance offers no server-provided SMS / voice.
func NewResolver(
	dbSvc db.Service, creds credentials.Service,
	instanceSender Sender, instanceVoice VoiceCaller, instanceSenderID string,
	opts ...ResolverOption,
) *Resolver {
	resolver := &Resolver{
		db: dbSvc, creds: creds,
		instanceSender: instanceSender, instanceVoice: instanceVoice,
		instanceSenderID:    instanceSenderID,
		orgSenderFactory:    NewOrgTwilioSender,
		orgSMSFactorFactory: NewOrgSMSFactorSender,
	}

	for _, opt := range opts {
		opt(resolver)
	}

	return resolver
}

// Resolve returns the provider(s) this org sends through.
//
// Resolution order, applied per capability: a per-org custom integration
// PROVIDING that capability wins; otherwise the instance-level config applies.
// For SMS the org's Twilio integration is preferred over its SMS Factor one,
// which is only looked up when the org has no Twilio integration.
// An org that has its own integration therefore never touches the instance
// credentials for a capability its own account covers — which is the whole
// point of bring-your-own, and the reason the guards in §6 can be scoped to
// the server path.
//
// Never returns an error for "nothing configured": that is ModeUnavailable
// with nil senders, so a missing provider degrades to a skip rather than
// failing an escalation step. An error here means the org's own integration
// exists but could not be opened (undecryptable envelope), which an operator
// must see.
func (r *Resolver) Resolve(ctx context.Context, orgUID string) (*Resolution, error) {
	res := &Resolution{
		SMSMode: ModeUnavailable, VoiceMode: ModeUnavailable,
		SenderID: r.instanceSenderID,
	}

	conn, settings, err := r.orgIntegration(ctx, orgUID)
	if err != nil {
		return nil, err
	}

	if conn != nil {
		res.Conn = conn

		byo := r.orgSenderFactory(settings)
		// A per-org Twilio integration always provides SMS: validateTwilioSettings
		// requires exactly one of from_number / messaging_service_sid.
		res.Sender = byo
		res.SMSMode = ModeBringYourOwn
		res.SenderID = settings.FromNumber

		if settings.FromNumber == "" {
			res.SenderID = settings.MessagingServiceSID
		}

		// Voice is only provided when the account has a caller ID configured.
		// Without one the capability falls through to the instance, matching
		// "a per-org integration providing the capability wins".
		if settings.VoiceFromNumber != "" {
			res.Voice = byo
			res.VoiceMode = ModeBringYourOwn
		}
	}

	if conn == nil {
		if err := r.resolveOrgSMSFactor(ctx, orgUID, res); err != nil {
			return nil, err
		}
	}

	if res.Sender == nil && r.instanceSender != nil {
		res.Sender = r.instanceSender
		res.SMSMode = ModeServer
		res.SenderID = r.instanceSenderID
	}

	if res.Voice == nil && r.instanceVoice != nil {
		res.Voice = r.instanceVoice
		res.VoiceMode = ModeServer
	}

	return res, nil
}

// orgIntegration returns the org's default enabled Twilio integration with
// decrypted settings, or (nil, nil, nil) when it has none.
func (r *Resolver) orgIntegration(
	ctx context.Context, orgUID string,
) (*models.Integration, *models.TwilioSettings, error) {
	conn, settings, err := twilioconn.ResolveDefault(ctx, r.db, r.creds, orgUID)
	if err != nil {
		if errors.Is(err, twilioconn.ErrNoTwilioConnection) {
			return nil, nil, nil
		}

		return nil, nil, fmt.Errorf("resolve org twilio integration: %w", err)
	}

	return conn, settings, nil
}

// resolveOrgSMSFactor makes the org's default enabled SMS Factor integration,
// when it has one, its bring-your-own SMS provider. Voice is untouched: SMS
// Factor has none, so it stays on the instance (or unavailable).
func (r *Resolver) resolveOrgSMSFactor(ctx context.Context, orgUID string, res *Resolution) error {
	conn, err := twilioconn.DefaultEnabledConnection(ctx, r.db, orgUID, models.ConnectionTypeSMSFactor)
	if err != nil {
		return fmt.Errorf("resolve org smsfactor integration: %w", err)
	}

	if conn == nil {
		return nil
	}

	raw, err := twilioconn.DecryptSettingsMap(ctx, r.creds, conn)
	if err != nil {
		return fmt.Errorf("resolve org smsfactor integration: %w", err)
	}

	settings, err := models.SMSFactorSettingsFromJSONMap(raw)
	if err != nil {
		return fmt.Errorf("parse smsfactor settings: %w", err)
	}

	res.Sender = r.orgSMSFactorFactory(settings)
	res.SMSMode = ModeBringYourOwn
	res.SenderID = settings.Sender

	return nil
}

// InstanceCallbackID is the sentinel `cid` used on Twilio callback URLs for a
// send made with the INSTANCE's credentials. A per-org send carries the
// integration UID instead. The callback middleware branches on this to decide
// which auth token validates Twilio's signature; it is deliberately a value no
// UID can collide with.
const InstanceCallbackID = "__instance__"
