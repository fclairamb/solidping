package usernotifications

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// The tests in this file pin the "every pageable type is testable" rule: the
// dashboard shows one generic Test button for any ready route, so every
// contact type an escalation can page must have a working case in
// dispatchTestRoute, and SendTestNotification must refuse contacts whose
// setup round-trip is not done.

// routeFor builds an in-memory route of the given contact type.
func routeFor(contactType, value string) *models.UserNotificationRoute {
	return &models.UserNotificationRoute{
		UID:     "route-uid",
		Enabled: true,
		Contact: &models.UserContact{
			UID:   "contact-uid",
			Type:  contactType,
			Value: value,
		},
	}
}

// setupTestRouteEnv builds a db-backed service, one org/user, and optionally
// the org's default Twilio connection.
func setupTestRouteEnv(t *testing.T, withTwilio bool) (*Service, db.Service, *models.Organization, *models.User) {
	t.Helper()

	ctx := context.Background()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("test-route-org", "Test Route Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	user := models.NewUser("oncall@example.com")
	require.NoError(t, dbSvc.CreateUser(ctx, user))

	member := models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)
	require.NoError(t, dbSvc.CreateOrganizationMember(ctx, member))

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

	return NewService(dbSvc, nil), dbSvc, org, user
}

// capturingTwilio records the form fields of every Messages.json POST.
type capturingTwilio struct {
	server *httptest.Server

	mu    sync.Mutex
	forms []map[string]string
}

func newCapturingTwilio(t *testing.T) *capturingTwilio {
	t.Helper()

	fake := &capturingTwilio{}

	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()

		form := map[string]string{}
		for k := range req.PostForm {
			form[k] = req.PostForm.Get(k)
		}

		fake.mu.Lock()
		fake.forms = append(fake.forms, form)
		fake.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sid":"SM1","status":"queued"}`))
	}))
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *capturingTwilio) sent() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]map[string]string{}, f.forms...)
}

// TestDispatchTestRoute_PhoneSendsSMS proves a phone route's test rides the
// org's default Twilio connection, exactly like a verification code does.
//
//nolint:paralleltest // mutates the package-level newTwilioClient seam.
func TestDispatchTestRoute_PhoneSendsSMS(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	svc, _, org, _ := setupTestRouteEnv(t, true)

	fake := newCapturingTwilio(t)
	withFakeVerifyTwilio(t, fake.server)

	err := svc.dispatchTestRoute(
		ctx, org.UID, org.Slug, routeFor(models.UserContactTypePhone, "+15551230000"),
		nil, nil, webpush.Options{},
	)
	r.NoError(err)

	sent := fake.sent()
	r.Len(sent, 1, "the test button must actually send an SMS")
	r.Equal("+15551230000", sent[0]["To"])
	r.Equal("+15559990000", sent[0]["From"])
	r.Contains(sent[0]["Body"], "Test notification")
}

// TestDispatchTestRoute_PhoneWithoutProviderIsNamed proves an org without a
// Twilio connection gets the specific "no SMS provider" error, not the generic
// unknown-contact-type default.
func TestDispatchTestRoute_PhoneWithoutProviderIsNamed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, _, org, _ := setupTestRouteEnv(t, false)

	err := svc.dispatchTestRoute(
		ctx, org.UID, org.Slug, routeFor(models.UserContactTypePhone, "+15551230000"),
		nil, nil, webpush.Options{},
	)
	r.ErrorIs(err, ErrNoProvider)
	r.NotContains(err.Error(), "provider not configured for contact type")
}

// fakeGraphAPI records template sends hitting a fake Meta Graph API.
type fakeGraphAPI struct {
	server *httptest.Server

	mu       sync.Mutex
	payloads []map[string]any
}

func newFakeGraphAPI(t *testing.T) *fakeGraphAPI {
	t.Helper()

	fake := &fakeGraphAPI{}

	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)

		payload := map[string]any{}
		_ = json.Unmarshal(body, &payload)

		fake.mu.Lock()
		fake.payloads = append(fake.payloads, payload)
		fake.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.TEST1"}]}`))
	}))
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *fakeGraphAPI) sent() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]map[string]any{}, f.payloads...)
}

// TestDispatchTestRoute_WhatsAppSendsAlertTemplate proves a WhatsApp route's
// test goes out as the approved alert template — the only business-initiated
// message Meta delivers outside a reply window — with self-describing test
// values in the body slots.
func TestDispatchTestRoute_WhatsAppSendsAlertTemplate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	fake := newFakeGraphAPI(t)

	svc := NewService(nil, nil, WithWhatsAppConfig(&config.WhatsAppConfig{
		Enabled:       true,
		AccessToken:   "meta-token",
		PhoneNumberID: "12345",
		BaseURL:       fake.server.URL,
	}))

	err := svc.dispatchTestRoute(
		ctx, "org-uid", "acme", routeFor(models.UserContactTypeWhatsApp, "+15551230000"),
		nil, nil, webpush.Options{},
	)
	r.NoError(err)

	sent := fake.sent()
	r.Len(sent, 1, "the test button must actually send a template message")
	// The client normalizes recipients to bare digits for the Cloud API.
	r.Equal("15551230000", sent[0]["to"])

	raw, marshalErr := json.Marshal(sent[0])
	r.NoError(marshalErr)
	r.Contains(string(raw), config.DefaultWhatsAppAlertTemplate,
		"the test must use the approved alert template")
	r.Contains(string(raw), "acme",
		"the org-slug body slot must carry the actual org")
}

// TestDispatchTestRoute_WhatsAppInactiveIsNamed proves an instance without
// WhatsApp gets the specific error, not the unknown-contact-type default.
func TestDispatchTestRoute_WhatsAppInactiveIsNamed(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	svc := NewService(nil, nil) // WhatsApp entirely unconfigured

	err := svc.dispatchTestRoute(
		context.Background(), "org-uid", "acme", routeFor(models.UserContactTypeWhatsApp, "+15551230000"),
		nil, nil, webpush.Options{},
	)
	r.ErrorIs(err, ErrNoWhatsAppProvider)
}

// TestSendTestNotification_UnverifiedContactRejected pins the readiness rule
// the dashboard's generic Test button relies on: a contact whose setup
// round-trip is not done (unverified phone, disconnected Telegram) must be
// refused, or the test button could text arbitrary unconfirmed numbers.
func TestSendTestNotification_UnverifiedContactRejected(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	svc, dbSvc, org, user := setupTestRouteEnv(t, true)

	// An unverified phone contact, exactly as CreateContact leaves it.
	created, err := svc.CreateContact(ctx, org.Slug, user, CreateContactRequest{
		Type:  models.UserContactTypePhone,
		Value: "+15551230000",
	})
	r.NoError(err)

	err = svc.SendTestNotification(ctx, org.Slug, user, created.UID, nil, nil, webpush.Options{})
	r.ErrorIs(err, ErrContactNotVerified)

	// A telegram contact whose connection was lost (VerifiedAt cleared, as a
	// blocked bot leaves behind) is refused the same way.
	tg := models.NewUserContact(user.UID, org.UID, models.UserContactTypeTelegram, "987654321", "@flo")
	r.NoError(dbSvc.UpsertUserContact(ctx, tg))
	r.NoError(dbSvc.EnsureUserNotificationRoute(ctx, user.UID, org.UID, tg.UID))

	routes, err := dbSvc.ListUserContactsWithRoutes(ctx, user.UID, org.UID)
	r.NoError(err)

	var tgRouteUID string

	for _, route := range routes {
		if route.Contact != nil && route.Contact.UID == tg.UID {
			tgRouteUID = route.UID
		}
	}

	r.NotEmpty(tgRouteUID)

	err = svc.SendTestNotification(ctx, org.Slug, user, tgRouteUID, nil, nil, webpush.Options{})
	r.ErrorIs(err, ErrContactNotVerified)
}

// TestSendTestNotification_VerifiedPhoneGoesThrough is the positive control
// for the rejection above: the same phone contact, once verified, sails
// through the public entry point and reaches Twilio.
//
//nolint:paralleltest // mutates the package-level newTwilioClient seam.
func TestSendTestNotification_VerifiedPhoneGoesThrough(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()

	svc, dbSvc, org, user := setupTestRouteEnv(t, true)

	fake := newCapturingTwilio(t)
	withFakeVerifyTwilio(t, fake.server)

	created, err := svc.CreateContact(ctx, org.Slug, user, CreateContactRequest{
		Type:  models.UserContactTypePhone,
		Value: "+15551230000",
	})
	r.NoError(err)
	r.NoError(dbSvc.MarkUserContactVerified(ctx, created.Contact.UID, time.Now()))

	err = svc.SendTestNotification(ctx, org.Slug, user, created.UID, nil, nil, webpush.Options{})
	r.NoError(err)
	r.Len(fake.sent(), 1)
}

// TestEveryPageableContactTypeHasATestCase closes the loop structurally: the
// escalation dispatcher and the test dispatcher must know the same contact
// types. A new pageable type that forgets its test case lands in the default
// branch and answers the Test button with "provider not configured" — the
// exact shipped bug this guards against.
func TestEveryPageableContactTypeHasATestCase(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	pageable := []string{
		models.UserContactTypeEmail,
		models.UserContactTypeSlackUser,
		models.UserContactTypeWebPush,
		models.UserContactTypePhone,
		models.UserContactTypeWhatsApp,
		models.UserContactTypeTelegram,
	}

	// A db-backed service with NO provider configured anywhere: every case
	// must fail with its own named "not configured" error, never the default.
	svc, _, org, _ := setupTestRouteEnv(t, false)

	for _, contactType := range pageable {
		err := svc.dispatchTestRoute(
			context.Background(), org.UID, org.Slug, routeFor(contactType, "value"),
			nil, nil, webpush.Options{},
		)
		// Reaching the default branch means the type has NO test case at all;
		// every real case fails differently here (unconfigured provider).
		if err != nil {
			r.NotContains(err.Error(), "provider not configured for contact type",
				"contact type %q is pageable but has no test-dispatch case", contactType)
		}
	}
}
