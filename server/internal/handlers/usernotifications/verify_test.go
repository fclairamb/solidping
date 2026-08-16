package usernotifications

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	smssvc "github.com/fclairamb/solidping/server/internal/integrations/sms"
)

// setupVerifyEnv builds a service backed by SQLite with an org, user, and an
// unverified phone contact. withTwilio adds a default Twilio integration.
func setupVerifyEnv(
	t *testing.T, withTwilio bool,
) (*Service, *models.Organization, *models.User, *models.UserContact, db.Service) {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("verify-org", "Verify Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("user@example.com")
	require.NoError(t, dbSvc.CreateUser(ctx, user))

	contact := models.NewUserContact(user.UID, org.UID, models.UserContactTypePhone, "+15551230000", "mobile")
	require.NoError(t, dbSvc.UpsertUserContact(ctx, contact))

	if withTwilio {
		integration := models.NewIntegration(org.UID, models.ConnectionTypeTwilio, "twilio")
		integration.Enabled = true
		integration.IsDefault = true
		integration.Settings = models.JSONMap{
			"account_sid": "AC00000000000000000000000000000001",
			"auth_token":  "tok",
			"from_number": "+15559990000",
		}
		require.NoError(t, dbSvc.CreateChannel(ctx, integration))
	}

	// The resolver is what picks between the org's own Twilio integration and
	// the instance provider. Neither is configured here beyond the optional
	// integration above, so an env built with withTwilio=false resolves to
	// nothing and the verify flow must report ErrNoProvider.
	resolver := smssvc.NewResolver(dbSvc, nil, nil, nil, "")

	return NewService(dbSvc, nil, WithSMSResolver(resolver)), org, user, contact, dbSvc
}

// fakeTwilioSMS captures verification-code sends.
func fakeTwilioSMS(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"SM1","status":"queued"}`))
	}))
	t.Cleanup(srv.Close)

	return srv
}

// withFakeVerifyTwilio repoints the service's bring-your-own sender factory at
// a fake Twilio. The seam is injected per service, not a package-level var, so
// parallel tests never race on it.
func withFakeVerifyTwilio(svc *Service, dbSvc db.Service, srv *httptest.Server) {
	svc.smsResolver = smssvc.NewResolver(dbSvc, nil, nil, nil, "",
		smssvc.WithOrgSenderFactory(func(settings *models.TwilioSettings) smssvc.PhoneSender {
			return smssvc.NewTwilioSender(&smssvc.TwilioCredentials{
				AccountSID: settings.AccountSID, AuthToken: settings.AuthToken,
				BaseURL: srv.URL, From: settings.FromNumber,
				MessagingServiceSID: settings.MessagingServiceSID,
				VoiceFrom:           settings.VoiceFromNumber,
			})
		}))
}

func TestVerifyContact_IssuesCode(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, org, user, contact, dbSvc := setupVerifyEnv(t, true)
	withFakeVerifyTwilio(svc, dbSvc, fakeTwilioSMS(t))

	r.NoError(svc.VerifyContact(ctx, org.Slug, user, contact.UID))

	// A code hash + expiry are now stored.
	reloaded, err := svc.db.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.NotNil(reloaded.VerifyCodeHash)
	r.NotEmpty(*reloaded.VerifyCodeHash)
	r.NotNil(reloaded.VerifyExpiresAt)
	r.Nil(reloaded.VerifiedAt)
}

func TestVerifyContact_NoProvider(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, org, user, contact, _ := setupVerifyEnv(t, false) // no twilio integration
	r.ErrorIs(svc.VerifyContact(ctx, org.Slug, user, contact.UID), ErrNoProvider)
}

// TestVerifyContact_RegionResolvesRegionalBase proves a bring-your-own
// connection's region still resolves to the matching Twilio host after the
// provider seam replaced the concrete-client threading. A silent fallback to
// the default US1 edge makes perfectly valid regional credentials look invalid.
func TestVerifyContact_RegionResolvesRegionalBase(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, org, user, contact, _ := setupVerifyEnv(t, true)
	r.NotNil(user)
	r.Nil(contact.VerifiedAt)

	channels, err := svc.db.ListChannels(ctx, &models.ListIntegrationsFilter{OrganizationUID: org.UID})
	r.NoError(err)
	r.Len(channels, 1)
	conn := channels[0]
	conn.Settings["region"] = "au1"
	r.NoError(svc.db.UpdateChannel(ctx, conn.UID, &models.IntegrationUpdate{Settings: &conn.Settings}))

	resolution, err := svc.smsResolver.Resolve(ctx, org.UID)
	r.NoError(err)
	r.Equal(smssvc.ModeBringYourOwn, resolution.SMSMode)

	settings, err := models.TwilioSettingsFromJSONMap(conn.Settings)
	r.NoError(err)
	r.Equal("https://api.au1.twilio.com",
		smssvc.TwilioBaseURL(&smssvc.TwilioCredentials{Region: settings.Region}),
		"phone verification must resolve the connection's region, not the default base")
}

func TestVerifyContact_ResendRateLimited(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, org, user, contact, dbSvc := setupVerifyEnv(t, true)
	withFakeVerifyTwilio(svc, dbSvc, fakeTwilioSMS(t))

	// verifyResendMax issuances allowed, the next one is throttled.
	for i := 0; i < verifyResendMax; i++ {
		r.NoError(svc.VerifyContact(ctx, org.Slug, user, contact.UID))
	}
	r.ErrorIs(svc.VerifyContact(ctx, org.Slug, user, contact.UID), ErrResendTooSoon)
}

// testVerifyCode is the fixed code stampCode writes so confirm can be driven
// deterministically.
const testVerifyCode = "123456"

// stampCode writes a known code hash + given expiry onto the contact.
func stampCode(t *testing.T, svc *Service, contactUID string, expiry time.Time) {
	t.Helper()

	h := hashCode(testVerifyCode)
	require.NoError(t, svc.db.SetUserContactVerifyState(context.Background(), contactUID, &h, &expiry, 0))
}

func TestConfirmVerify_Success(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	svc, org, user, contact, _ := setupVerifyEnv(t, true)
	stampCode(t, svc, contact.UID, time.Now().Add(5*time.Minute))

	r.NoError(svc.ConfirmVerify(ctx, org.Slug, user, contact.UID, testVerifyCode))

	reloaded, err := svc.db.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.NotNil(reloaded.VerifiedAt)
	r.Nil(reloaded.VerifyCodeHash)
}

func TestConfirmVerify_NoPendingCode(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	svc, org, user, contact, _ := setupVerifyEnv(t, true)
	r.ErrorIs(svc.ConfirmVerify(ctx, org.Slug, user, contact.UID, testVerifyCode), ErrNoPendingCode)
}

func TestConfirmVerify_Expired(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	svc, org, user, contact, _ := setupVerifyEnv(t, true)
	stampCode(t, svc, contact.UID, time.Now().Add(-time.Minute))

	r.ErrorIs(svc.ConfirmVerify(ctx, org.Slug, user, contact.UID, testVerifyCode), ErrCodeExpired)
}

func TestConfirmVerify_WrongCodeIncrementsThenCaps(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	svc, org, user, contact, _ := setupVerifyEnv(t, true)
	stampCode(t, svc, contact.UID, time.Now().Add(5*time.Minute))

	// verifyMaxAttempts wrong tries, each a mismatch.
	for i := 0; i < verifyMaxAttempts; i++ {
		r.ErrorIs(svc.ConfirmVerify(ctx, org.Slug, user, contact.UID, "000000"), ErrCodeMismatch)
	}

	// Code is now invalidated: even the correct code yields no-pending-code.
	r.ErrorIs(svc.ConfirmVerify(ctx, org.Slug, user, contact.UID, testVerifyCode), ErrNoPendingCode)

	reloaded, err := svc.db.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.Nil(reloaded.VerifyCodeHash, "burnt code must be cleared")
	r.Nil(reloaded.VerifiedAt, "must not be verified after too many attempts")
}

func TestConfirmVerify_ForeignContactRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	svc, org, _, contact, _ := setupVerifyEnv(t, true)
	stampCode(t, svc, contact.UID, time.Now().Add(5*time.Minute))

	// A different user must not confirm someone else's contact.
	otherUser := models.NewUser("intruder@example.com")
	r.NoError(svc.db.CreateUser(ctx, otherUser))

	r.ErrorIs(svc.ConfirmVerify(ctx, org.Slug, otherUser, contact.UID, testVerifyCode), ErrContactNotFound)
}
