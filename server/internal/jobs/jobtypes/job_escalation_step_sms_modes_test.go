package jobtypes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	smssvc "github.com/fclairamb/solidping/server/internal/integrations/sms"
)

// fakeOVHServer is a stand-in for the OVH SMS API, wired in through
// SP_SMS_OVH_BASE_URL exactly as production would reach the real one — no code
// path differs between the two.
type fakeOVHServer struct {
	server *httptest.Server

	mu    sync.Mutex
	jobs  []map[string]any
	calls int
}

func newFakeOVHServer(t *testing.T) *fakeOVHServer {
	t.Helper()

	fake := &fakeOVHServer{}
	mux := http.NewServeMux()

	mux.HandleFunc("/1.0/auth/time", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "%d", 1700000000)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)

		fake.mu.Lock()
		fake.jobs = append(fake.jobs, decoded)
		fake.calls++
		fake.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ids":[8801],"validReceivers":["+15551230000"]}`))
	})

	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *fakeOVHServer) sentJobs() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]map[string]any{}, f.jobs...)
}

// instanceTwilioSender builds the real instance-level Twilio sender against a
// fake API, through the same config path production uses.
func instanceTwilioSender(t *testing.T, base string) smssvc.Sender {
	t.Helper()

	cfg := &config.SMSConfig{
		Enabled:  true,
		Provider: config.SMSProviderTwilio,
		Sender:   "+15550001111",
		Twilio: config.SMSTwilioConfig{
			AccountSID: "AC00000000000000000000000000000009",
			AuthToken:  "instance-token",
			BaseURL:    base,
		},
	}
	require.True(t, cfg.Active())

	sender, err := smssvc.NewInstanceSender(t.Context(), cfg)
	require.NoError(t, err)
	require.NotNil(t, sender)

	return sender
}

// instanceOVHSender builds the real instance-level OVH sender against a fake
// API, through SP_SMS_OVH_BASE_URL's config field.
func instanceOVHSender(t *testing.T, base string) smssvc.Sender {
	t.Helper()

	cfg := &config.SMSConfig{
		Enabled:  true,
		Provider: config.SMSProviderOVH,
		Sender:   "SolidPing",
		OVH: config.SMSOVHConfig{
			Endpoint: "ovh-eu", BaseURL: base + "/1.0",
			ApplicationKey: "ak", ApplicationSecret: "as",
			ConsumerKey: "ck", ServiceName: "sms-ab12345-1",
		},
	}
	require.True(t, cfg.Active())

	sender, err := smssvc.NewInstanceSender(t.Context(), cfg)
	require.NoError(t, err)
	require.NotNil(t, sender)

	return sender
}

func instanceTwilioVoice(t *testing.T, base string) smssvc.VoiceCaller {
	t.Helper()

	cfg := &config.VoiceConfig{
		Enabled:    true,
		FromNumber: "+15550002222",
		Twilio: config.SMSTwilioConfig{
			AccountSID: "AC00000000000000000000000000000009",
			AuthToken:  "instance-voice-token",
			BaseURL:    base,
		},
	}
	require.True(t, cfg.Active())

	voice, err := smssvc.NewInstanceVoiceCaller(cfg)
	require.NoError(t, err)
	require.NotNil(t, voice)

	return voice
}

func smsFilter() map[string]bool { return map[string]bool{"sms": true} }

// TestBringYourOwnNeverTouchesInstanceCredentials is the central negative
// control of the two-mode design: an org that has brought its own Twilio
// account must send through it, and the instance fake must receive NOTHING.
// Silently spending the instance's money on an org that pays for its own is a
// billing bug that no functional test would notice.
func TestBringYourOwnNeverTouchesInstanceCredentials(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, true, "") // org HAS its own Twilio integration
	orgFake, orgSrv := newFakeTwilio(t)
	env.useFakeTwilio(orgSrv)

	instanceFake, instanceSrv := newFakeTwilio(t)
	env.useInstanceSender(instanceTwilioSender(t, instanceSrv.URL), nil, "+15550001111")

	run := newRun()
	sent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), smsFilter())

	r.Equal(1, sent)
	r.Equal(1, orgFake.smsCount(), "the org's own Twilio account must be the one used")
	r.Equal(0, instanceFake.smsCount(),
		"a bring-your-own org must never reach the instance credentials")
}

// TestNoOrgIntegrationUsesInstanceProvider is the other half: an org with no
// integration of its own is NOT left resolving to nothing — that is the whole
// point of removing the requirement to bring your own account.
func TestNoOrgIntegrationUsesInstanceProvider(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "") // no org integration at all
	instanceFake, instanceSrv := newFakeTwilio(t)
	env.useInstanceSender(instanceTwilioSender(t, instanceSrv.URL), nil, "+15550001111")

	run := newRun()
	sent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), smsFilter())

	r.Equal(1, sent)
	r.Equal(1, instanceFake.smsCount())
	r.Equal("+15550001111", instanceFake.lastForm(t).Get("From"),
		"the server-provided path sends under the one instance-wide sender")
}

// TestDeletingOrgIntegrationFallsBackToInstance proves the documented
// deletion behaviour the UI promises: removing the bring-your-own integration
// drops the org back to the server-provided mode on the next send, rather than
// leaving it unable to page anyone.
func TestDeletingOrgIntegrationFallsBackToInstance(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, true, "")
	orgFake, orgSrv := newFakeTwilio(t)
	instanceFake, instanceSrv := newFakeTwilio(t)
	env.useInstanceSender(instanceTwilioSender(t, instanceSrv.URL), nil, "+15550001111")
	env.useFakeTwilio(orgSrv)

	run := newRun()
	r.Equal(1, run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), smsFilter()))
	r.Equal(1, orgFake.smsCount())
	r.Equal(0, instanceFake.smsCount())

	// Delete the bring-your-own integration.
	channels, err := env.db.ListChannels(ctx, &models.ListIntegrationsFilter{OrganizationUID: env.org.UID})
	r.NoError(err)
	r.Len(channels, 1)
	r.NoError(env.db.DeleteChannel(ctx, channels[0].UID))

	// A fresh run (the dedup map is per run) now resolves to the instance.
	run = newRun()
	r.Equal(1, run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), smsFilter()))
	r.Equal(1, orgFake.smsCount(), "the deleted integration must not be used again")
	r.Equal(1, instanceFake.smsCount(), "the org falls back to the server-provided mode")
}

// TestOVHForSMSAndTwilioForVoiceResolveIndependently is the configuration we
// expect to ship: the cheap provider for SMS, Twilio for the calls OVH cannot
// place. Binding the two would mean losing escalation calls in exchange for
// cheaper SMS.
func TestOVHForSMSAndTwilioForVoiceResolveIndependently(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	ovhFake := newFakeOVHServer(t)
	twilioFake, twilioSrv := newFakeTwilio(t)

	env.useInstanceSender(
		instanceOVHSender(t, ovhFake.server.URL),
		instanceTwilioVoice(t, twilioSrv.URL),
		"SolidPing",
	)

	run := newRun()
	sent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), map[string]bool{"sms": true, "voice": true})

	r.Equal(2, sent, "both capabilities fire, from two different providers")

	jobs := ovhFake.sentJobs()
	r.Len(jobs, 1, "the SMS must go to OVH")
	r.Equal("SolidPing", jobs[0]["sender"])
	r.Equal(true, jobs[0]["noStopClause"],
		"noStopClause must always be true end-to-end: without it OVH appends a "+
			"STOP clause that can double the segment cost, silently")

	r.Equal(0, twilioFake.smsCount(), "Twilio must not have been used for the SMS")
	r.Equal(1, twilioFake.callCount(), "the voice call must go to Twilio")
}

// TestInstanceGuardsRefuseDisallowedCountry is a negative control: the send
// must be REFUSED, not merely counted. An out-of-allow-list destination that
// still goes out is exactly the expensive line the guard exists to prevent.
func TestInstanceGuardsRefuseDisallowedCountry(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	instanceFake, instanceSrv := newFakeTwilio(t)
	env.useInstanceSender(instanceTwilioSender(t, instanceSrv.URL), nil, "+15550001111")
	env.jctx.Services.Entitlements = entitlements.NewService(
		env.db, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0,
		// Only France allowed; the verified route is a +1 number.
		entitlements.WithInstanceSMSGuards(100, []string{"33"}),
	)

	run := newRun()
	sent := run.dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), smsFilter())

	r.Equal(0, sent, "a disallowed destination must be refused")
	r.Equal(0, instanceFake.smsCount(), "nothing may reach the provider")

	// The breach surfaces on the org Usage page, not only in the logs.
	status := env.jctx.Services.Entitlements.SMSGuardStatusFor(env.org.UID)
	r.NotNil(status)
	r.Equal(1, status.CountryBlockedBreaches)
	r.True(status.Breached())
}

// TestInstanceGuardsRefuseOverCap is the instance-wide-cap counterpart: once
// exhausted, the send is refused rather than counted.
func TestInstanceGuardsRefuseOverCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	instanceFake, instanceSrv := newFakeTwilio(t)
	env.useInstanceSender(instanceTwilioSender(t, instanceSrv.URL), nil, "+15550001111")
	env.jctx.Services.Entitlements = entitlements.NewService(
		env.db, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0,
		entitlements.WithInstanceSMSGuards(1, nil),
	)

	// First send goes out.
	r.Equal(1, newRun().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), smsFilter()))
	r.Equal(1, instanceFake.smsCount())

	// The second is refused — the cap is instance-wide, so a fresh run for the
	// same org (or any other org) hits it.
	r.Equal(0, newRun().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), smsFilter()))
	r.Equal(1, instanceFake.smsCount(), "the over-cap send must never reach the provider")

	status := env.jctx.Services.Entitlements.SMSGuardStatusFor(env.org.UID)
	r.Equal(1, status.GlobalRunawayBreaches)
}

// TestBringYourOwnIsExemptFromInstanceGuards is the guard-scoping control. A
// bring-your-own send bills the customer's own account, so it must neither
// consume the instance-wide cap nor be blocked by an allow-list chosen for
// this instance's bill — while the per-org runaway guard still applies to it.
func TestBringYourOwnIsExemptFromInstanceGuards(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, true, "") // bring-your-own
	orgFake, orgSrv := newFakeTwilio(t)
	env.useFakeTwilio(orgSrv)

	instanceFake, instanceSrv := newFakeTwilio(t)
	env.useInstanceSender(instanceTwilioSender(t, instanceSrv.URL), nil, "+15550001111")

	entSvc := entitlements.NewService(
		env.db, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0,
		// A cap of one, and an allow-list that excludes the +1 destination.
		// Neither may touch a bring-your-own send.
		entitlements.WithInstanceSMSGuards(1, []string{"33"}),
	)
	env.jctx.Services.Entitlements = entSvc

	for i := range 3 {
		sent := newRun().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
			verifiedPhoneRoute(env.org.UID), smsFilter())
		r.Equal(1, sent, "bring-your-own send %d must not be blocked by an instance guard", i+1)
	}

	r.Equal(3, orgFake.smsCount())
	r.Equal(0, instanceFake.smsCount())

	// No breach was ever recorded: the guards were not even consulted.
	status := entSvc.SMSGuardStatusFor(env.org.UID)
	r.NotNil(status)
	r.Equal(0, status.GlobalRunawayBreaches)
	r.Equal(0, status.CountryBlockedBreaches)
	r.False(status.Breached())
}

// TestNoProviderAtAllDegradesToSkip proves the "no org is ever left resolving
// to nothing" promise fails safe rather than loudly: with neither mode
// configured the phone route is skipped, not errored.
func TestNoProviderAtAllDegradesToSkip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")

	sent := newRun().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
		verifiedPhoneRoute(env.org.UID), smsFilter())
	r.Equal(0, sent)
}

// lastForm returns the most recent captured Twilio form.
func (f *fakeTwilio) lastForm(t *testing.T) url.Values {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	require.NotEmpty(t, f.forms)

	return f.forms[len(f.forms)-1]
}

// TestPerOrgCapRefusesSendInBothModes pins the third guard's scope. Unlike the
// two instance-spend guards, the per-org runaway cap applies to EVERY send
// whoever pays — including a bring-your-own one — and it too must refuse
// rather than merely count.
func TestPerOrgCapRefusesSendInBothModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		withOrgInt bool
	}{
		{"server-provided", false},
		{"bring-your-own", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx := context.Background()

			env := setupPhoneEnv(t, tt.withOrgInt, "")
			orgFake, orgSrv := newFakeTwilio(t)
			env.useFakeTwilio(orgSrv)

			instanceFake, instanceSrv := newFakeTwilio(t)
			env.useInstanceSender(instanceTwilioSender(t, instanceSrv.URL), nil, "+15550001111")

			// A per-org cap of one SMS/hour, and no instance-spend guard at all
			// so only the per-org one can be responsible for a refusal.
			env.jctx.Services.Entitlements = entitlements.NewService(
				env.db, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0,
				entitlements.WithRunawayCaps(1, 10),
			)

			total := func() int { return orgFake.smsCount() + instanceFake.smsCount() }

			r.Equal(1, newRun().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
				verifiedPhoneRoute(env.org.UID), smsFilter()))
			r.Equal(1, total())

			r.Equal(0, newRun().dispatchRoute(ctx, env.jctx, slog.Default(), env.incident,
				verifiedPhoneRoute(env.org.UID), smsFilter()),
				"the per-org cap must refuse the second send")
			r.Equal(1, total(), "the refused send must never reach any provider")
		})
	}
}
