package config

import (
	"os"
	"strings"
)

// SMS provider identifiers accepted by SP_SMS_PROVIDER.
const (
	// SMSProviderTwilio selects the Twilio REST API as the instance SMS provider.
	SMSProviderTwilio = "twilio"
	// SMSProviderOVH selects the OVHcloud SMS API as the instance SMS provider.
	SMSProviderOVH = "ovh"
)

// DefaultSMSGlobalRunawayPerHour bounds instance-wide SMS spend on the
// server's own credentials. The per-org guard (30/h) is respected by every
// org individually, so 200 orgs can legitimately add up to 6 000 SMS/h of
// *instance* money — this cap is the only thing that sees the total. Set
// deliberately high enough that it never fires in normal operation and low
// enough that a dispatch bug costs tens of euros rather than thousands.
const DefaultSMSGlobalRunawayPerHour = 500

// SMSTwilioConfig holds the instance-level Twilio credentials used when
// Provider is "twilio". AuthToken is a SECRET: env/SSM only, never logged,
// never returned by any API, never sent to a browser.
type SMSTwilioConfig struct {
	// AccountSID is the AC… account identifier.
	AccountSID string `koanf:"account_sid"`
	// AuthToken authenticates the REST calls. SECRET.
	AuthToken string `koanf:"auth_token"`
	// Region is the Twilio region token (us1, ie1, au1…). Empty means the
	// default US1 edge. Validated with twilio.ValidRegion at startup.
	Region string `koanf:"region"`
	// MessagingServiceSID, when set, sends through a Messaging Service rather
	// than a bare from-number. Takes precedence over SMSConfig.Sender.
	MessagingServiceSID string `koanf:"messaging_service_sid"`
	// BaseURL overrides the Twilio REST base (SP_SMS_TWILIO_BASE_URL). Empty
	// means the region-derived real host. Exists so test mode / E2E can point
	// the whole feature at a fake API without any code path differing —
	// the SP_WHATSAPP_BASE_URL precedent.
	BaseURL string `koanf:"base_url"`
}

// SMSOVHConfig holds the instance-level OVHcloud SMS credentials used when
// Provider is "ovh". ApplicationSecret and ConsumerKey are SECRETS.
type SMSOVHConfig struct {
	// Endpoint is the OVH API region: ovh-eu, ovh-ca or ovh-us.
	Endpoint string `koanf:"endpoint"`
	// ApplicationKey identifies the OVH application.
	ApplicationKey string `koanf:"application_key"`
	// ApplicationSecret signs the requests. SECRET.
	ApplicationSecret string `koanf:"application_secret"`
	// ConsumerKey is the validated per-user credential. SECRET.
	ConsumerKey string `koanf:"consumer_key"`
	// ServiceName is the sms-xxxxx-1 account the jobs are posted to.
	ServiceName string `koanf:"service_name"`
	// BaseURL overrides the endpoint-derived API base
	// (SP_SMS_OVH_BASE_URL) for tests / an egress proxy.
	BaseURL string `koanf:"base_url"`
	// DLRToken is the high-entropy path segment on the delivery-receipt
	// callback URL. OVH does NOT sign its callbacks and its API carries no
	// per-job callback field — the callback URL is a service-level setting on
	// the SMS account — so the endpoint authenticates by construction with
	// this token instead. SECRET-ish: it is an authenticator, never logged.
	// Empty means "derive one deterministically from the JWT secret", which
	// keeps a self-hoster from having to invent one while staying rotatable.
	DLRToken string `koanf:"dlr_token"`
}

// SMSConfig contains the instance-level SMS configuration: which provider the
// server itself sends through, under which sender identity, and the two
// instance-spend guards.
//
// Mirrors WhatsAppConfig exactly — one deployment-wide identity, secrets from
// env/SSM only, and a single Active() enablement rule. The difference from
// WhatsApp is that a per-org bring-your-own override still exists (a per-org
// Twilio integration, see the resolver): this block is what an org gets when
// it has not brought its own account, which is the default and needs no setup.
//
// Default Enabled:false. Every credential field is a SECRET: env/SSM only,
// never logged, never returned by any API, never sent to a browser.
type SMSConfig struct {
	// Enabled is the kill switch (SP_SMS_ENABLED). It only ever turns the
	// feature off — credentials are still required to turn it on (see Active).
	Enabled bool `koanf:"enabled"`
	// Provider selects the backend: "twilio" or "ovh". Empty defaults to
	// "twilio" so an operator who supplies only Twilio credentials works.
	Provider string `koanf:"provider"`
	// Sender is the instance-wide sender identity: an E.164 number for Twilio,
	// a registered alphanumeric sender (or short number) for OVH. There is
	// deliberately no per-org override on this path — an org that wants its
	// own sender brings its own Twilio integration instead.
	Sender string `koanf:"sender"`
	// GlobalRunawayPerHour is the instance-wide hourly cap on sends made with
	// the SERVER's credentials (SP_SMS_GLOBAL_RUNAWAY_PER_HOUR). It guards
	// instance spend, so bring-your-own sends never consume it. Zero means the
	// DefaultSMSGlobalRunawayPerHour default; negative disables the cap.
	GlobalRunawayPerHour int `koanf:"global_runaway_per_hour"`
	// AllowedCountries is a comma-separated allow-list of E.164 country
	// calling codes (e.g. "33,1,44"), empty meaning "all countries".
	// A count quota under-prices expensive destinations — France is ~0,05 €
	// while some destinations exceed 0,30 € — so the quota alone cannot bound
	// the bill. Like the global cap it gates SERVER-credential sends only.
	AllowedCountries string `koanf:"allowed_countries"`

	Twilio SMSTwilioConfig `koanf:"twilio"`
	OVH    SMSOVHConfig    `koanf:"ovh"`
}

// ResolvedProvider returns the configured provider, defaulting to Twilio.
func (c *SMSConfig) ResolvedProvider() string {
	if p := strings.ToLower(strings.TrimSpace(c.Provider)); p != "" {
		return p
	}

	return SMSProviderTwilio
}

// Active reports whether the instance can actually send an SMS on its own
// credentials. This is THE enablement rule, applied identically by the
// escalation dispatcher, the verification flow, the test-send button and the
// public config endpoint: the kill switch must be on AND the credentials that
// a send cannot work without must be present for the selected provider.
// Anything else is off — in particular Enabled=true with no credentials.
func (c *SMSConfig) Active() bool {
	if !c.Enabled || strings.TrimSpace(c.Sender) == "" {
		return false
	}

	switch c.ResolvedProvider() {
	case SMSProviderOVH:
		return strings.TrimSpace(c.OVH.ApplicationKey) != "" &&
			strings.TrimSpace(c.OVH.ApplicationSecret) != "" &&
			strings.TrimSpace(c.OVH.ConsumerKey) != "" &&
			strings.TrimSpace(c.OVH.ServiceName) != ""
	case SMSProviderTwilio:
		return strings.TrimSpace(c.Twilio.AccountSID) != "" &&
			strings.TrimSpace(c.Twilio.AuthToken) != ""
	default:
		return false
	}
}

// ResolvedGlobalRunawayPerHour returns the effective instance-wide hourly cap:
// the configured value, the default when unset, and 0 (meaning "no cap") when
// explicitly negative.
func (c *SMSConfig) ResolvedGlobalRunawayPerHour() int {
	switch {
	case c.GlobalRunawayPerHour > 0:
		return c.GlobalRunawayPerHour
	case c.GlobalRunawayPerHour < 0:
		return 0
	default:
		return DefaultSMSGlobalRunawayPerHour
	}
}

// AllowedCountryCodes parses AllowedCountries into a normalized list of E.164
// country calling codes (digits only, no '+'). An empty result means "all
// countries allowed".
func (c *SMSConfig) AllowedCountryCodes() []string {
	raw := strings.Split(c.AllowedCountries, ",")
	out := make([]string, 0, len(raw))

	for _, item := range raw {
		code := strings.TrimPrefix(strings.TrimSpace(item), "+")
		if code != "" {
			out = append(out, code)
		}
	}

	return out
}

// VoiceConfig contains the instance-level voice (outbound call) credentials.
//
// Deliberately independent of SMSConfig: OVH has no voice API, so binding the
// two would mean losing escalation calls in exchange for cheaper SMS. Voice is
// Twilio-only and stays Twilio-only; an instance can therefore run OVH for SMS
// and Twilio for voice at the same time, which is the configuration we expect
// to ship.
//
// Default Enabled:false. AuthToken is a SECRET.
type VoiceConfig struct {
	// Enabled is the kill switch (SP_VOICE_ENABLED).
	Enabled bool `koanf:"enabled"`
	// FromNumber is the instance-wide E.164 caller ID.
	FromNumber string `koanf:"from_number"`

	Twilio SMSTwilioConfig `koanf:"twilio"`
}

// Active reports whether the instance can actually place a call on its own
// credentials. Same contract as SMSConfig.Active.
func (c *VoiceConfig) Active() bool {
	return c.Enabled &&
		strings.TrimSpace(c.FromNumber) != "" &&
		strings.TrimSpace(c.Twilio.AccountSID) != "" &&
		strings.TrimSpace(c.Twilio.AuthToken) != ""
}

// SP_SMS_* / SP_VOICE_* environment variable names. Declared once and
// referenced from both the manual readers below and the RecognizedEnvVars list
// in envvars.go, so the two can never drift apart.
const (
	EnvSMSEnabled              = "SP_SMS_ENABLED"
	EnvSMSProvider             = "SP_SMS_PROVIDER"
	EnvSMSSender               = "SP_SMS_SENDER"
	EnvSMSGlobalRunawayPerHour = "SP_SMS_GLOBAL_RUNAWAY_PER_HOUR"
	EnvSMSAllowedCountries     = "SP_SMS_ALLOWED_COUNTRIES"

	EnvSMSTwilioAccountSID          = "SP_SMS_TWILIO_ACCOUNT_SID"
	EnvSMSTwilioAuthToken           = "SP_SMS_TWILIO_AUTH_TOKEN"
	EnvSMSTwilioRegion              = "SP_SMS_TWILIO_REGION"
	EnvSMSTwilioMessagingServiceSID = "SP_SMS_TWILIO_MESSAGING_SERVICE_SID"
	EnvSMSTwilioBaseURL             = "SP_SMS_TWILIO_BASE_URL"

	EnvSMSOVHEndpoint          = "SP_SMS_OVH_ENDPOINT"
	EnvSMSOVHApplicationKey    = "SP_SMS_OVH_APPLICATION_KEY"
	EnvSMSOVHApplicationSecret = "SP_SMS_OVH_APPLICATION_SECRET"
	EnvSMSOVHConsumerKey       = "SP_SMS_OVH_CONSUMER_KEY"
	EnvSMSOVHServiceName       = "SP_SMS_OVH_SERVICE_NAME"
	EnvSMSOVHBaseURL           = "SP_SMS_OVH_BASE_URL"
	EnvSMSOVHDLRToken          = "SP_SMS_OVH_DLR_TOKEN"

	EnvVoiceEnabled          = "SP_VOICE_ENABLED"
	EnvVoiceFromNumber       = "SP_VOICE_FROM_NUMBER"
	EnvVoiceTwilioAccountSID = "SP_VOICE_TWILIO_ACCOUNT_SID"
	EnvVoiceTwilioAuthToken  = "SP_VOICE_TWILIO_AUTH_TOKEN"
	EnvVoiceTwilioRegion     = "SP_VOICE_TWILIO_REGION"
	EnvVoiceTwilioBaseURL    = "SP_VOICE_TWILIO_BASE_URL"
)

// SMSEnvVarNames lists every manually-read SP_SMS_* string name, in the order
// applySMSEnv binds them. The two int/bool switches are read separately.
func SMSEnvVarNames() []string {
	return []string{
		EnvSMSProvider, EnvSMSSender, EnvSMSAllowedCountries,
		EnvSMSTwilioAccountSID, EnvSMSTwilioAuthToken, EnvSMSTwilioRegion,
		EnvSMSTwilioMessagingServiceSID, EnvSMSTwilioBaseURL,
		EnvSMSOVHEndpoint, EnvSMSOVHApplicationKey, EnvSMSOVHApplicationSecret,
		EnvSMSOVHConsumerKey, EnvSMSOVHServiceName, EnvSMSOVHBaseURL,
		EnvSMSOVHDLRToken,
	}
}

// VoiceEnvVarNames lists every manually-read SP_VOICE_* string name, in the
// order applyVoiceEnv binds them.
func VoiceEnvVarNames() []string {
	return []string{
		EnvVoiceFromNumber,
		EnvVoiceTwilioAccountSID, EnvVoiceTwilioAuthToken,
		EnvVoiceTwilioRegion, EnvVoiceTwilioBaseURL,
	}
}

// AllSMSEnvVarNames is every SP_SMS_*/SP_VOICE_* name config.Load binds,
// including the kill switches and the numeric cap. Used by RecognizedEnvVars.
func AllSMSEnvVarNames() []string {
	names := SMSEnvVarNames()
	out := make([]string, 0, len(names)+len(VoiceEnvVarNames())+3)
	out = append(out, names...)
	out = append(out, VoiceEnvVarNames()...)
	out = append(out, EnvSMSEnabled, EnvSMSGlobalRunawayPerHour, EnvVoiceEnabled)

	return out
}

// applySMSEnv reads SP_SMS_* into cfg. Most keys here have a snake_case
// segment that koanf's underscore→dot collapsing can never reach
// (SP_SMS_TWILIO_ACCOUNT_SID would land on sms.twilio.account.sid), so the
// whole block is read by hand — the same quirk as rate_limiting, posthog,
// whatsapp and telegram. The handful koanf CAN reach are re-read here too so
// there is exactly one place that binds SMS configuration.
// Keep in sync with manualReaderPlatformEnvVars.
func applySMSEnv(cfg *SMSConfig) {
	targets := []*string{
		&cfg.Provider, &cfg.Sender, &cfg.AllowedCountries,
		&cfg.Twilio.AccountSID, &cfg.Twilio.AuthToken, &cfg.Twilio.Region,
		&cfg.Twilio.MessagingServiceSID, &cfg.Twilio.BaseURL,
		&cfg.OVH.Endpoint, &cfg.OVH.ApplicationKey, &cfg.OVH.ApplicationSecret,
		&cfg.OVH.ConsumerKey, &cfg.OVH.ServiceName, &cfg.OVH.BaseURL,
		&cfg.OVH.DLRToken,
	}

	for i, name := range SMSEnvVarNames() {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			*targets[i] = v
		}
	}

	if v := strings.TrimSpace(os.Getenv(EnvSMSEnabled)); v != "" {
		cfg.Enabled = v == envTrue || v == "1"
	}

	// envInt returns 0 for "unset", which ResolvedGlobalRunawayPerHour reads as
	// "use the default" — so a negative value (the documented "no cap") has to
	// be bound explicitly rather than through the >0 shorthand used elsewhere.
	if raw := strings.TrimSpace(os.Getenv(EnvSMSGlobalRunawayPerHour)); raw != "" {
		cfg.GlobalRunawayPerHour = envInt(EnvSMSGlobalRunawayPerHour)
	}
}

// applyVoiceEnv reads SP_VOICE_* into cfg. Same koanf quirk as applySMSEnv.
// Keep in sync with manualReaderPlatformEnvVars.
func applyVoiceEnv(cfg *VoiceConfig) {
	targets := []*string{
		&cfg.FromNumber,
		&cfg.Twilio.AccountSID, &cfg.Twilio.AuthToken,
		&cfg.Twilio.Region, &cfg.Twilio.BaseURL,
	}

	for i, name := range VoiceEnvVarNames() {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			*targets[i] = v
		}
	}

	if v := strings.TrimSpace(os.Getenv(EnvVoiceEnabled)); v != "" {
		cfg.Enabled = v == envTrue || v == "1"
	}
}
