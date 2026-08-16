package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSMSEnvVarsBind proves every SP_SMS_*/SP_VOICE_* name actually lands on
// its struct field. Almost every one of these keys has a snake_case koanf tag
// that koanf's env provider can never reach on its own
// (SP_SMS_TWILIO_ACCOUNT_SID collapses to sms.twilio.account.sid), so without
// applySMSEnv/applyVoiceEnv they would be silent no-ops — the exact failure
// mode this test exists to catch.
func TestSMSEnvVarsBind(t *testing.T) {
	t.Setenv("SP_SMS_ENABLED", "true")
	t.Setenv("SP_SMS_PROVIDER", "ovh")
	t.Setenv("SP_SMS_SENDER", "SolidPing")
	t.Setenv("SP_SMS_GLOBAL_RUNAWAY_PER_HOUR", "77")
	t.Setenv("SP_SMS_ALLOWED_COUNTRIES", "33, +1 ,44")
	t.Setenv("SP_SMS_TWILIO_ACCOUNT_SID", "AC00000000000000000000000000000001")
	t.Setenv("SP_SMS_TWILIO_AUTH_TOKEN", "twilio-token")
	t.Setenv("SP_SMS_TWILIO_REGION", "ie1")
	t.Setenv("SP_SMS_TWILIO_MESSAGING_SERVICE_SID", "MG0000000000000000000000000000001")
	t.Setenv("SP_SMS_TWILIO_BASE_URL", "https://twilio.test")
	t.Setenv("SP_SMS_OVH_ENDPOINT", "ovh-eu")
	t.Setenv("SP_SMS_OVH_APPLICATION_KEY", "appkey")
	t.Setenv("SP_SMS_OVH_APPLICATION_SECRET", "appsecret")
	t.Setenv("SP_SMS_OVH_CONSUMER_KEY", "consumerkey")
	t.Setenv("SP_SMS_OVH_SERVICE_NAME", "sms-ab12345-1")
	t.Setenv("SP_SMS_OVH_BASE_URL", "https://ovh.test")
	t.Setenv("SP_SMS_OVH_DLR_TOKEN", "dlr-token-value")
	t.Setenv("SP_VOICE_ENABLED", "true")
	t.Setenv("SP_VOICE_FROM_NUMBER", "+15551234567")
	t.Setenv("SP_VOICE_TWILIO_ACCOUNT_SID", "AC00000000000000000000000000000002")
	t.Setenv("SP_VOICE_TWILIO_AUTH_TOKEN", "voice-token")
	t.Setenv("SP_VOICE_TWILIO_REGION", "us1")
	t.Setenv("SP_VOICE_TWILIO_BASE_URL", "https://voice.test")

	r := require.New(t)
	cfg, err := Load()
	r.NoError(err)

	r.True(cfg.SMS.Enabled)
	r.Equal("ovh", cfg.SMS.ResolvedProvider())
	r.Equal("SolidPing", cfg.SMS.Sender)
	r.Equal(77, cfg.SMS.ResolvedGlobalRunawayPerHour())
	r.Equal([]string{"33", "1", "44"}, cfg.SMS.AllowedCountryCodes())
	r.Equal("AC00000000000000000000000000000001", cfg.SMS.Twilio.AccountSID)
	r.Equal("twilio-token", cfg.SMS.Twilio.AuthToken)
	r.Equal("ie1", cfg.SMS.Twilio.Region)
	r.Equal("MG0000000000000000000000000000001", cfg.SMS.Twilio.MessagingServiceSID)
	r.Equal("https://twilio.test", cfg.SMS.Twilio.BaseURL)
	r.Equal("ovh-eu", cfg.SMS.OVH.Endpoint)
	r.Equal("appkey", cfg.SMS.OVH.ApplicationKey)
	r.Equal("appsecret", cfg.SMS.OVH.ApplicationSecret)
	r.Equal("consumerkey", cfg.SMS.OVH.ConsumerKey)
	r.Equal("sms-ab12345-1", cfg.SMS.OVH.ServiceName)
	r.Equal("https://ovh.test", cfg.SMS.OVH.BaseURL)
	r.Equal("dlr-token-value", cfg.SMS.OVH.DLRToken)
	r.True(cfg.SMS.Active())

	r.True(cfg.Voice.Enabled)
	r.Equal("+15551234567", cfg.Voice.FromNumber)
	r.Equal("AC00000000000000000000000000000002", cfg.Voice.Twilio.AccountSID)
	r.Equal("voice-token", cfg.Voice.Twilio.AuthToken)
	r.Equal("us1", cfg.Voice.Twilio.Region)
	r.Equal("https://voice.test", cfg.Voice.Twilio.BaseURL)
	r.True(cfg.Voice.Active())

	// Every name used above must also be advertised as recognized, otherwise
	// the startup env check would flag a variable that in fact binds.
	recognized := RecognizedEnvVars()
	for _, name := range AllSMSEnvVarNames() {
		r.Contains(recognized, name)
	}
}

// TestSMSDefaultsOff proves the feature is dark out of the box and that the
// kill switch alone cannot turn it on.
func TestSMSDefaultsOff(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg, err := Load()
	r.NoError(err)
	r.False(cfg.SMS.Enabled)
	r.False(cfg.SMS.Active())
	r.False(cfg.Voice.Enabled)
	r.False(cfg.Voice.Active())
	r.Equal(SMSProviderTwilio, cfg.SMS.ResolvedProvider())
	r.Equal(DefaultSMSGlobalRunawayPerHour, cfg.SMS.ResolvedGlobalRunawayPerHour())
	r.Empty(cfg.SMS.AllowedCountryCodes())
}

func TestSMSConfigActive(t *testing.T) {
	t.Parallel()

	full := func(mutate func(*SMSConfig)) SMSConfig {
		cfg := SMSConfig{
			Enabled:  true,
			Provider: SMSProviderTwilio,
			Sender:   "+15551234567",
			Twilio:   SMSTwilioConfig{AccountSID: "AC1", AuthToken: "tok"},
			OVH: SMSOVHConfig{
				ApplicationKey: "ak", ApplicationSecret: "as",
				ConsumerKey: "ck", ServiceName: "sms-1",
			},
		}
		if mutate != nil {
			mutate(&cfg)
		}

		return cfg
	}

	tests := []struct {
		name   string
		mutate func(*SMSConfig)
		want   bool
	}{
		{"twilio complete", nil, true},
		{"ovh complete", func(c *SMSConfig) { c.Provider = SMSProviderOVH }, true},
		{"kill switch off", func(c *SMSConfig) { c.Enabled = false }, false},
		{"no sender", func(c *SMSConfig) { c.Sender = " " }, false},
		{"twilio missing sid", func(c *SMSConfig) { c.Twilio.AccountSID = "" }, false},
		{"twilio missing token", func(c *SMSConfig) { c.Twilio.AuthToken = "" }, false},
		{"ovh missing consumer key", func(c *SMSConfig) {
			c.Provider = SMSProviderOVH
			c.OVH.ConsumerKey = ""
		}, false},
		{"ovh missing service name", func(c *SMSConfig) {
			c.Provider = SMSProviderOVH
			c.OVH.ServiceName = ""
		}, false},
		{"unknown provider", func(c *SMSConfig) { c.Provider = "nexmo" }, false},
		{"provider casing is normalized", func(c *SMSConfig) { c.Provider = "OVH" }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := full(tt.mutate)
			require.Equal(t, tt.want, cfg.Active())
		})
	}
}

func TestSMSGlobalRunawayDisable(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal(0, (&SMSConfig{GlobalRunawayPerHour: -1}).ResolvedGlobalRunawayPerHour())
	r.Equal(DefaultSMSGlobalRunawayPerHour, (&SMSConfig{}).ResolvedGlobalRunawayPerHour())
	r.Equal(12, (&SMSConfig{GlobalRunawayPerHour: 12}).ResolvedGlobalRunawayPerHour())
}

// TestSMSGlobalRunawayDisableViaEnv proves the documented "negative disables
// the cap" value survives the env reader — envInt's zero-for-unset return
// would otherwise silently turn -1 into "use the default".
func TestSMSGlobalRunawayDisableViaEnv(t *testing.T) {
	t.Setenv("SP_SMS_GLOBAL_RUNAWAY_PER_HOUR", "-1")

	r := require.New(t)
	cfg, err := Load()
	r.NoError(err)
	r.Equal(0, cfg.SMS.ResolvedGlobalRunawayPerHour())
}
