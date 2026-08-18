package jobtypes

import (
	"context"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	smssvc "github.com/fclairamb/solidping/server/internal/integrations/sms"
)

// statusCallbackOf returns the StatusCallback parameter of the last request the
// fake Twilio API received, parsed. Asserting on what the PROVIDER was actually
// asked for is the whole point: the URL builder was correct for the
// bring-your-own case it was written for, so a test that only exercised the
// builder would have passed against the defect.
func statusCallbackOf(t *testing.T, fake *fakeTwilio) *url.URL {
	t.Helper()

	raw := fake.lastForm(t).Get("StatusCallback")
	require.NotEmpty(t, raw,
		"the send must ASK Twilio for a delivery receipt; without a StatusCallback "+
			"parameter Twilio never posts one and the notification row stays in its "+
			"initial state forever")

	parsed, err := url.Parse(raw)
	require.NoError(t, err)

	return parsed
}

// TestServerCredentialSMSRequestsDeliveryReceipt pins the reported bug: a send
// on the INSTANCE's own credentials (the default mode, no org setup at all)
// used to request no status callback whatsoever.
func TestServerCredentialSMSRequestsDeliveryReceipt(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "") // no org integration: server-provided mode
	env.jctx.AppConfig.SMS.Provider = config.SMSProviderTwilio

	instanceFake, instanceSrv := newFakeTwilio(t)
	env.useInstanceSender(instanceTwilioSender(t, instanceSrv.URL), nil, "+15550001111")

	r.Equal(1, newRun().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), smsFilter()))
	r.Equal(1, instanceFake.smsCount())

	cb := statusCallbackOf(t, instanceFake)
	r.Equal("/api/v1/integrations/twilio/status", cb.Path)
	r.Equal(smssvc.InstanceCallbackID, cb.Query().Get("cid"),
		"a server-credential send must carry the instance sentinel")
	r.Equal(env.org.UID, cb.Query().Get("oid"),
		"the org must travel inside the SIGNED url, never in the payload")
	r.True(strings.HasPrefix(cb.String(), "https://app.example.com/"))
}

// TestServerCredentialVoiceRequestsDeliveryReceipt is the voice half of the
// same defect, and additionally pins that the status URL and the TwiML URL of
// one call always name the same credentials.
func TestServerCredentialVoiceRequestsDeliveryReceipt(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	instanceFake, instanceSrv := newFakeTwilio(t)
	env.useInstanceSender(nil, instanceTwilioVoice(t, instanceSrv.URL), "+15550001111")

	r.Equal(1, newRun().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), map[string]bool{"voice": true}))
	r.Equal(1, instanceFake.callCount())

	form := instanceFake.lastForm(t)
	cb := statusCallbackOf(t, instanceFake)
	r.Equal(smssvc.InstanceCallbackID, cb.Query().Get("cid"))
	r.Equal(env.org.UID, cb.Query().Get("oid"))

	twiml, err := url.Parse(form.Get("Url"))
	r.NoError(err)
	r.Equal(cb.Query().Get("cid"), twiml.Query().Get("cid"),
		"the status url and the TwiML url of one call must name the same credentials")
	r.Equal(cb.Query().Get("oid"), twiml.Query().Get("oid"))
}

// TestVoiceOnInstanceCredentialsUnderOrgIntegrationUsesSentinel covers the
// asymmetric org: it brought its own Twilio account but configured no caller
// id, so voice falls through to the instance. The call is then placed with the
// INSTANCE's credentials, so its status callback must carry the sentinel — not
// the connection UID, which would be validated against the wrong auth token.
func TestVoiceOnInstanceCredentialsUnderOrgIntegrationUsesSentinel(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, true, "") // org integration WITHOUT voice_from_number
	instanceFake, instanceSrv := newFakeTwilio(t)
	orgFake, orgSrv := newFakeTwilio(t)
	env.useFakeTwilio(orgSrv)
	env.useInstanceSender(nil, instanceTwilioVoice(t, instanceSrv.URL), "+15550001111")

	r.Equal(1, newRun().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), map[string]bool{"voice": true}))
	r.Equal(1, instanceFake.callCount())
	r.Equal(0, orgFake.callCount())

	cb := statusCallbackOf(t, instanceFake)
	r.Equal(smssvc.InstanceCallbackID, cb.Query().Get("cid"))
}

// TestBringYourOwnStatusCallbackIsUnchanged is the regression control on the
// mode that already worked: the callback still names the connection.
func TestBringYourOwnStatusCallbackIsUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, true, "+15559991111") // org integration with voice too
	orgFake, orgSrv := newFakeTwilio(t)
	env.useFakeTwilio(orgSrv)

	conns, err := env.db.ListChannels(ctx, &models.ListIntegrationsFilter{OrganizationUID: env.org.UID})
	r.NoError(err)
	r.Len(conns, 1)

	r.Equal(2, newRun().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), map[string]bool{"sms": true, "voice": true}))
	r.Equal(1, orgFake.smsCount())
	r.Equal(1, orgFake.callCount())

	for _, form := range orgFake.allForms() {
		cb, parseErr := url.Parse(form.Get("StatusCallback"))
		r.NoError(parseErr)
		r.Equal(conns[0].UID, cb.Query().Get("cid"),
			"a bring-your-own callback keeps naming the connection")
	}
}

// TestOVHSMSRequestsNoTwilioStatusCallback is the positive control for the
// explicitly supported OVH-for-SMS + Twilio-for-voice pairing: the SMS must ask
// for no Twilio receipt at all (OVH has no per-job callback field and its
// receipts arrive on the service-level DLR endpoint), while the voice call
// still asks for one.
func TestOVHSMSRequestsNoTwilioStatusCallback(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	env.jctx.AppConfig.SMS.Provider = config.SMSProviderOVH

	ovhFake := newFakeOVHServer(t)
	twilioFake, twilioSrv := newFakeTwilio(t)
	env.useInstanceSender(
		instanceOVHSender(t, ovhFake.server.URL),
		instanceTwilioVoice(t, twilioSrv.URL),
		"SolidPing",
	)

	r.Equal(2, newRun().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), map[string]bool{"sms": true, "voice": true}))

	jobs := ovhFake.sentJobs()
	r.Len(jobs, 1)

	for key, value := range jobs[0] {
		if text, ok := value.(string); ok {
			r.NotContains(text, "/api/v1/integrations/twilio/status",
				"the OVH job must carry no Twilio status callback (field %q)", key)
		}
	}

	// The builder itself refuses, rather than relying on the OVH client to drop
	// a callback it was handed: a receipt nothing can deliver or validate must
	// never be requested in the first place.
	cfg := &config.Config{}
	cfg.SMS.Provider = config.SMSProviderOVH
	r.Empty(smsStatusCallbackURL(cfg, "https://app.example.com", env.org.UID,
		&smssvc.Resolution{SMSMode: smssvc.ModeServer}))

	r.Equal(0, twilioFake.smsCount())
	r.Equal(1, twilioFake.callCount())

	cb := statusCallbackOf(t, twilioFake)
	r.Equal(smssvc.InstanceCallbackID, cb.Query().Get("cid"),
		"the Twilio voice call still requests a receipt on the instance sentinel")
}

// TestStatusCallbackURLNeedsABaseURL keeps the "unreachable base url means no
// callback" rule: there is no point asking for a receipt nothing can post to.
func TestStatusCallbackURLNeedsABaseURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &config.Config{}
	r.Empty(smsStatusCallbackURL(cfg, "", "org-1", &smssvc.Resolution{SMSMode: smssvc.ModeServer}))
	r.Empty(voiceStatusCallbackURL("", "org-1", &smssvc.Resolution{VoiceMode: smssvc.ModeServer}))
}
