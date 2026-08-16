package usernotifications

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	smssvc "github.com/fclairamb/solidping/server/internal/integrations/sms"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// The verification code and the test button are outbound SMS on exactly the
// same bill as an escalation page. Before this, they resolved a sender and
// sent without ever consulting the instance-spend guards — so on server
// credentials they consumed neither the instance-wide cap nor the country
// allow-list. Verification is gated only by a per-contact in-memory resend
// limiter, which means any org member could add a premium-rate destination and
// request codes on the instance's money. Stopping exactly that is why
// SP_SMS_ALLOWED_COUNTRIES exists.

// countingTwilio records every Messages.json POST it receives.
type countingTwilio struct {
	server *httptest.Server

	mu    sync.Mutex
	forms []url.Values
}

func newCountingTwilio(t *testing.T) *countingTwilio {
	t.Helper()

	fake := &countingTwilio{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()

		fake.mu.Lock()
		fake.forms = append(fake.forms, r.PostForm)
		fake.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"SM_guard","status":"queued"}`))
	}))
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *countingTwilio) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.forms)
}

// guardEnv wires a service with BOTH an instance-level provider and (optionally)
// a per-org bring-your-own one, each behind its own fake, so a test can assert
// which of the two actually received a message.
type guardEnv struct {
	svc      *Service
	db       db.Service
	org      *models.Organization
	user     *models.User
	contact  *models.UserContact
	instance *countingTwilio
	own      *countingTwilio
	ent      *entitlements.Service
}

func setupGuardEnv(
	t *testing.T, withOwnIntegration bool, contactValue string, guards entitlements.Option,
) *guardEnv {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("guard-org", "Guard Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("oncall@example.com")
	require.NoError(t, dbSvc.CreateUser(ctx, user))
	require.NoError(t, dbSvc.CreateOrganizationMember(
		ctx, models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	contact := models.NewUserContact(
		user.UID, org.UID, models.UserContactTypePhone, contactValue, "mobile")
	require.NoError(t, dbSvc.UpsertUserContact(ctx, contact))

	own := newCountingTwilio(t)

	if withOwnIntegration {
		integration := models.NewIntegration(org.UID, models.ConnectionTypeTwilio, "twilio")
		integration.Enabled = true
		integration.IsDefault = true
		integration.Settings = models.JSONMap{
			"account_sid": "AC00000000000000000000000000000001",
			"auth_token":  "own-token",
			"from_number": "+15559990000",
		}
		require.NoError(t, dbSvc.CreateChannel(ctx, integration))
	}

	instance := newCountingTwilio(t)

	smsCfg := &config.SMSConfig{
		Enabled:  true,
		Provider: config.SMSProviderTwilio,
		Sender:   "+15550001111",
		Twilio: config.SMSTwilioConfig{
			AccountSID: "AC00000000000000000000000000000009",
			AuthToken:  "instance-token",
			BaseURL:    instance.server.URL,
		},
	}
	require.True(t, smsCfg.Active())

	instanceSender, err := smssvc.NewInstanceSender(t.Context(), smsCfg)
	require.NoError(t, err)

	resolver := smssvc.NewResolver(dbSvc, nil, instanceSender, nil, smsCfg.Sender,
		smssvc.WithOrgSenderFactory(func(settings *models.TwilioSettings) smssvc.PhoneSender {
			return smssvc.NewTwilioSender(&smssvc.TwilioCredentials{
				AccountSID: settings.AccountSID, AuthToken: settings.AuthToken,
				BaseURL: own.server.URL, From: settings.FromNumber,
				MessagingServiceSID: settings.MessagingServiceSID,
			})
		}))

	entSvc := entitlements.NewService(
		dbSvc, entitlements.DefaultsFor(config.DeploymentModeSelfHosted), 0, guards,
	)

	svc := NewService(dbSvc, nil, WithSMSResolver(resolver), WithEntitlements(entSvc))

	return &guardEnv{
		svc: svc, db: dbSvc, org: org, user: user, contact: contact,
		instance: instance, own: own, ent: entSvc,
	}
}

// TestVerificationRefusedForDisallowedCountry is a negative control on the
// verification path: an out-of-allow-list destination must be REFUSED, and
// nothing may reach the provider. A premium-rate destination that still
// receives a code is precisely the spend the allow-list exists to stop.
func TestVerificationRefusedForDisallowedCountry(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupGuardEnv(t, false, "+8801711111111",
		entitlements.WithInstanceSMSGuards(100, []string{"33", "1"}))

	err := env.svc.VerifyContact(ctx, env.org.Slug, env.user, env.contact.UID)
	r.ErrorIs(err, ErrSMSDestinationNotAllowed)
	r.Equal(0, env.instance.count(), "nothing may reach the instance provider")

	// No code was stamped either: refusing after stamping would burn a code and
	// a resend slot for a message that was never going to be sent.
	reloaded, err := env.db.GetUserContact(ctx, env.contact.UID)
	r.NoError(err)
	r.Nil(reloaded.VerifyCodeHash)

	// The breach surfaces on the org's Usage page, not only in the logs.
	status := env.ent.SMSGuardStatusFor(env.org.UID)
	r.NotNil(status)
	r.Equal(1, status.CountryBlockedBreaches)
	r.True(status.Breached())
}

// TestVerificationAllowedCountryStillWorks is the positive control for the
// test above: the same flow, an allowed destination, actually sends.
func TestVerificationAllowedCountryStillWorks(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupGuardEnv(t, false, "+15551230000",
		entitlements.WithInstanceSMSGuards(100, []string{"1"}))

	r.NoError(env.svc.VerifyContact(ctx, env.org.Slug, env.user, env.contact.UID))
	r.Equal(1, env.instance.count())
	r.Equal("+15550001111", env.instance.forms[0].Get("From"))
}

// TestVerificationRefusedByInstanceCap covers the other instance guard on the
// same path.
func TestVerificationRefusedByInstanceCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupGuardEnv(t, false, "+15551230000",
		entitlements.WithInstanceSMSGuards(1, nil))

	// Burn the single instance token on an unrelated org's send.
	r.NoError(env.ent.ReserveInstanceSMS(ctx, "some-other-org", "+15551230000"))

	err := env.svc.VerifyContact(ctx, env.org.Slug, env.user, env.contact.UID)
	r.ErrorIs(err, ErrSMSInstanceCapReached)
	r.Equal(0, env.instance.count(), "an over-cap verification must never reach the provider")
}

// TestTestSendRefusedByInstanceCap proves the test button is guarded too — it
// is a one-click way to spend the instance's money otherwise.
func TestTestSendRefusedByInstanceCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupGuardEnv(t, false, "+15551230000",
		entitlements.WithInstanceSMSGuards(1, nil))

	route := routeFor(models.UserContactTypePhone, "+15551230000")

	// The first test-send consumes the only instance token.
	r.NoError(env.svc.dispatchTestRoute(
		ctx, env.org.UID, env.org.Slug, route, nil, nil, webpush.Options{}))
	r.Equal(1, env.instance.count())

	// The second is refused outright.
	err := env.svc.dispatchTestRoute(
		ctx, env.org.UID, env.org.Slug, route, nil, nil, webpush.Options{})
	r.ErrorIs(err, ErrSMSInstanceCapReached)
	r.Equal(1, env.instance.count(), "the refused test-send must never reach the provider")

	status := env.ent.SMSGuardStatusFor(env.org.UID)
	r.Equal(1, status.GlobalRunawayBreaches)
}

// TestTestSendRefusedForDisallowedCountry is the allow-list control on the
// test-send path.
func TestTestSendRefusedForDisallowedCountry(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	env := setupGuardEnv(t, false, "+8801711111111",
		entitlements.WithInstanceSMSGuards(100, []string{"33"}))

	err := env.svc.dispatchTestRoute(
		ctx, env.org.UID, env.org.Slug,
		routeFor(models.UserContactTypePhone, "+8801711111111"),
		nil, nil, webpush.Options{},
	)
	r.ErrorIs(err, ErrSMSDestinationNotAllowed)
	r.Equal(0, env.instance.count())
}

// TestBringYourOwnExemptFromInstanceGuardsOnBothPaths is the scoping control
// for this fix: applying the guards to verification and test-send must not
// over-apply them. A bring-your-own org bills its own Twilio account, so
// neither the instance-wide cap nor the country allow-list may touch it — on
// these two paths exactly as on the escalation path.
func TestBringYourOwnExemptFromInstanceGuardsOnBothPaths(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	// A cap of one AND an allow-list that excludes the +880 destination:
	// either would refuse a server-credential send.
	env := setupGuardEnv(t, true, "+8801711111111",
		entitlements.WithInstanceSMSGuards(1, []string{"33"}))

	r.NoError(env.svc.VerifyContact(ctx, env.org.Slug, env.user, env.contact.UID))

	route := routeFor(models.UserContactTypePhone, "+8801711111111")
	for range 3 {
		r.NoError(env.svc.dispatchTestRoute(
			ctx, env.org.UID, env.org.Slug, route, nil, nil, webpush.Options{}))
	}

	r.Equal(4, env.own.count(), "every send must go through the org's own account")
	r.Equal(0, env.instance.count(), "none may reach the instance credentials")

	// No breach was recorded: the guards were never even consulted.
	status := env.ent.SMSGuardStatusFor(env.org.UID)
	r.NotNil(status)
	r.Equal(0, status.GlobalRunawayBreaches)
	r.Equal(0, status.CountryBlockedBreaches)
	r.False(status.Breached())
}
