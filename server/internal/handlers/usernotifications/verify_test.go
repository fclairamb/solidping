package usernotifications

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/integrations/twilio"
)

// setupVerifyEnv builds a service backed by SQLite with an org, user, and an
// unverified phone contact. withTwilio adds a default Twilio integration.
func setupVerifyEnv(t *testing.T, withTwilio bool) (*Service, *models.Organization, *models.User, *models.UserContact) {
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

	return NewService(dbSvc, nil), org, user, contact
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

func withFakeVerifyTwilio(t *testing.T, srv *httptest.Server) {
	t.Helper()

	prev := newTwilioClient
	newTwilioClient = func(accountSID, authToken, _ string) *twilio.Client {
		return twilio.NewClientWithBaseURL(accountSID, authToken, srv.URL)
	}
	t.Cleanup(func() { newTwilioClient = prev })
}

//nolint:paralleltest // mutates the package-level newTwilioClient seam.
func TestVerifyContact_IssuesCode(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	svc, org, user, contact := setupVerifyEnv(t, true)
	withFakeVerifyTwilio(t, fakeTwilioSMS(t))

	r.NoError(svc.VerifyContact(ctx, org.Slug, user, contact.UID))

	// A code hash + expiry are now stored.
	reloaded, err := svc.db.GetUserContact(ctx, contact.UID)
	r.NoError(err)
	r.NotNil(reloaded.VerifyCodeHash)
	r.NotEmpty(*reloaded.VerifyCodeHash)
	r.NotNil(reloaded.VerifyExpiresAt)
	r.Nil(reloaded.VerifiedAt)
}

//nolint:paralleltest // mutates the package-level newTwilioClient seam.
func TestVerifyContact_NoProvider(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	svc, org, user, contact := setupVerifyEnv(t, false) // no twilio integration
	r.ErrorIs(svc.VerifyContact(ctx, org.Slug, user, contact.UID), ErrNoProvider)
}

// TestVerifyContact_RegionResolvesRegionalBase proves the phone-verification
// send resolves the connection's region to the matching Twilio host — the
// third of the three call sites this spec fixes (the other two are the
// escalation SMS and voice paths in jobtypes).
//
//nolint:paralleltest // mutates the package-level newTwilioClient seam.
func TestVerifyContact_RegionResolvesRegionalBase(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	svc, org, user, contact := setupVerifyEnv(t, true)

	channels, err := svc.db.ListChannels(ctx, &models.ListIntegrationsFilter{OrganizationUID: org.UID})
	r.NoError(err)
	r.Len(channels, 1)
	conn := channels[0]
	conn.Settings["region"] = "au1"
	r.NoError(svc.db.UpdateChannel(ctx, conn.UID, &models.IntegrationUpdate{Settings: &conn.Settings}))

	srv := fakeTwilioSMS(t)

	var gotBaseURL string
	prev := newTwilioClient
	newTwilioClient = func(accountSID, authToken, baseURL string) *twilio.Client {
		gotBaseURL = baseURL

		return twilio.NewClientWithBaseURL(accountSID, authToken, srv.URL)
	}
	t.Cleanup(func() { newTwilioClient = prev })

	r.NoError(svc.VerifyContact(ctx, org.Slug, user, contact.UID))
	r.Equal("https://api.au1.twilio.com", gotBaseURL,
		"phone verification must resolve the connection's region, not the default base")
}

//nolint:paralleltest // mutates the package-level newTwilioClient seam.
func TestVerifyContact_ResendRateLimited(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	svc, org, user, contact := setupVerifyEnv(t, true)
	withFakeVerifyTwilio(t, fakeTwilioSMS(t))

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

	svc, org, user, contact := setupVerifyEnv(t, true)
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

	svc, org, user, contact := setupVerifyEnv(t, true)
	r.ErrorIs(svc.ConfirmVerify(ctx, org.Slug, user, contact.UID, testVerifyCode), ErrNoPendingCode)
}

func TestConfirmVerify_Expired(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	svc, org, user, contact := setupVerifyEnv(t, true)
	stampCode(t, svc, contact.UID, time.Now().Add(-time.Minute))

	r.ErrorIs(svc.ConfirmVerify(ctx, org.Slug, user, contact.UID, testVerifyCode), ErrCodeExpired)
}

func TestConfirmVerify_WrongCodeIncrementsThenCaps(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	svc, org, user, contact := setupVerifyEnv(t, true)
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

	svc, org, _, contact := setupVerifyEnv(t, true)
	stampCode(t, svc, contact.UID, time.Now().Add(5*time.Minute))

	// A different user must not confirm someone else's contact.
	otherUser := models.NewUser("intruder@example.com")
	r.NoError(svc.db.CreateUser(ctx, otherUser))

	r.ErrorIs(svc.ConfirmVerify(ctx, org.Slug, otherUser, contact.UID, testVerifyCode), ErrContactNotFound)
}
