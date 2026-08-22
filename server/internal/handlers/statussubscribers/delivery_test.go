package statussubscribers_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/statussubscribers"
	"github.com/fclairamb/solidping/server/internal/servicesig"
)

// capturedRequest is one delivery a fake receiver saw.
type capturedRequest struct {
	Path      string
	Body      []byte
	Signature string
	Timestamp string
	KeyID     string
}

// receiver is a fake webhook endpoint. `status` is what it answers with, so a
// test can make it fail on demand.
type receiver struct {
	mu       sync.Mutex
	requests []capturedRequest
	status   int
	server   *httptest.Server
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()

	rec := &receiver{status: http.StatusOK}
	// TLS, because a delivery URL must be https — that rule is production
	// behavior under test here, not an obstacle to work around.
	rec.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		rec.mu.Lock()
		rec.requests = append(rec.requests, capturedRequest{
			Path:      r.URL.Path,
			Body:      body,
			Signature: r.Header.Get(servicesig.HeaderSignature),
			Timestamp: r.Header.Get(servicesig.HeaderTimestamp),
			KeyID:     r.Header.Get(servicesig.HeaderKeyID),
		})
		status := rec.status
		rec.mu.Unlock()

		w.WriteHeader(status)
	}))

	t.Cleanup(rec.server.Close)

	return rec
}

func (r *receiver) setStatus(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = status
}

func (r *receiver) captured() []capturedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]capturedRequest, len(r.requests))
	copy(out, r.requests)

	return out
}

// endpointSubscriber registers a WEBHOOK subscription pointing at url.
//
// Webhook rather than slack on purpose: the create path refuses a `slack` row
// that does not point at hooks.slack.com, which an httptest server never can.
// TestSlackDeliveryUsesTheSlackShape flips the channel after creation to reach
// the slack rendering, and TestEndpointValidation covers the host rule itself.
func endpointSubscriber(
	t *testing.T, s *subSetup, url, secret string,
) *statussubscribers.CreateEndpointSubscriberResponse {
	t.Helper()

	created, err := s.svc.CreateEndpointSubscriber(t.Context(), s.org.Slug, s.page.UID,
		&statussubscribers.CreateEndpointSubscriberRequest{
			Channel: "webhook", URL: url, SigningSecret: &secret,
		})
	require.NoError(t, err)

	return created
}

// deliverOnce runs one fan-out wave through a notifier pointed at the test
// server's client.
func deliverOnce(t *testing.T, s *subSetup, rec *receiver) {
	t.Helper()

	notifier := statussubscribers.NewNotifier(
		s.dbSvc, nil, nil, "https://status.example.com", nil, nil)
	// The receiver's own client, so the httptest TLS certificate verifies.
	notifier.SetHTTPClient(rec.server.Client())

	notifier.NotifyStatusUpdate(t.Context(), statussubscribers.UpdateEventFor(
		s.page.UID, nil, string(models.StatusUpdateKindInfo),
		"Database degraded", "We are investigating.", nil, "Acme Status", 0))
}

// TestWebhookDeliveryIsSignedAndCarriesOnlyPublicFields is the core of the
// webhook channel: the payload reaches the receiver, it is signed with the
// scheme servicesig pins, and it carries nothing the public page does not.
func TestWebhookDeliveryIsSignedAndCarriesOnlyPublicFields(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newSubSetup(t)
	rec := newReceiver(t)

	const secret = "shared-signing-secret"

	endpointSubscriber(t, s, rec.server.URL+"/hooks/abcdef123456", secret)
	deliverOnce(t, s, rec)

	got := rec.captured()
	r.Len(got, 1)
	r.Equal("/hooks/abcdef123456", got[0].Path)
	r.Equal("status-page-subscriber", got[0].KeyID)

	// Recompute the HMAC independently rather than calling Sign against itself.
	timestamp, err := strconv.ParseInt(got[0].Timestamp, 10, 64)
	r.NoError(err)

	digest := sha256.Sum256(got[0].Body)
	canonical := got[0].Timestamp + ".POST." + got[0].Path + "." + hex.EncodeToString(digest[:])
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(canonical))
	want := servicesig.SignaturePrefix + base64.StdEncoding.EncodeToString(mac.Sum(nil))

	r.Equal(want, got[0].Signature)
	r.NotZero(timestamp)

	var payload statussubscribers.WebhookPayload
	r.NoError(json.Unmarshal(got[0].Body, &payload))
	r.Equal("status_update.published", payload.Event)
	r.Equal("Acme Status", payload.PageName)
	r.Equal("Database degraded", payload.Title)

	// Never-public rule: no internal identity may ride along.
	body := string(got[0].Body)
	r.NotContains(body, "checkUid")
	r.NotContains(body, "output")
	r.NotContains(body, "organizationUid")
}

// TestSlackDeliveryUsesTheSlackShape checks the other channel renders Slack's
// incoming-webhook body rather than the generic one.
func TestSlackDeliveryUsesTheSlackShape(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newSubSetup(t)
	rec := newReceiver(t)

	// The Slack host check is on the CREATE path, so this test registers a
	// slack row directly against the local receiver via the webhook channel's
	// URL rules and then rewrites the channel — see the create-path test below
	// for the host validation itself.
	created := endpointSubscriber(t, s, rec.server.URL+"/services/T000/B000/xyz", "sec")

	sub, err := s.dbSvc.GetSubscriber(t.Context(), s.page.UID, created.UID)
	r.NoError(err)
	sub.Channel = models.SubscriberChannelSlack
	_, err = s.dbSvc.DB().NewUpdate().Model(sub).Column("channel").WherePK().Exec(t.Context())
	r.NoError(err)

	deliverOnce(t, s, rec)

	got := rec.captured()
	r.Len(got, 1)

	var payload struct {
		Text   string           `json:"text"`
		Blocks []map[string]any `json:"blocks"`
	}

	r.NoError(json.Unmarshal(got[0].Body, &payload))
	r.Equal("Acme Status: Database degraded", payload.Text)
	r.NotEmpty(payload.Blocks)
}

// TestDeliveryFailuresDisableTheSubscriptionAndRecordAnEvent is the circuit
// breaker: a receiver that keeps failing eventually stops being called, and
// the operator gets an event saying so instead of silence.
func TestDeliveryFailuresDisableTheSubscriptionAndRecordAnEvent(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newSubSetup(t)
	rec := newReceiver(t)
	rec.setStatus(http.StatusInternalServerError)

	created := endpointSubscriber(t, s, rec.server.URL+"/hooks/dead", "sec")

	// Five consecutive failures is the documented threshold.
	for range 5 {
		deliverOnce(t, s, rec)
	}

	sub, err := s.dbSvc.GetSubscriber(t.Context(), s.page.UID, created.UID)
	r.NoError(err)
	r.Equal(5, sub.FailureCount)
	r.NotNil(sub.DisabledAt, "the breaker must trip at the threshold")

	events, err := s.dbSvc.ListEvents(t.Context(), &models.ListEventsFilter{
		OrganizationUID: s.org.UID,
		EventTypes:      []models.EventType{models.EventTypeStatusSubscriberDisabled},
	})
	r.NoError(err)
	r.Len(events, 1, "a disabled subscription must be visible as an event, not just a flag")
	r.Equal(created.UID, events[0].Payload["subscriberUid"])

	// And it really stops being called.
	before := len(rec.captured())
	deliverOnce(t, s, rec)
	r.Len(rec.captured(), before, "a disabled subscription must not be retried")
}

// TestOneSuccessResetsTheFailureCounter: a receiver that blips during a deploy
// must never be disabled.
func TestOneSuccessResetsTheFailureCounter(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newSubSetup(t)
	rec := newReceiver(t)
	rec.setStatus(http.StatusBadGateway)

	created := endpointSubscriber(t, s, rec.server.URL+"/hooks/flaky", "sec")

	for range 4 {
		deliverOnce(t, s, rec)
	}

	sub, err := s.dbSvc.GetSubscriber(t.Context(), s.page.UID, created.UID)
	r.NoError(err)
	r.Equal(4, sub.FailureCount)
	r.Nil(sub.DisabledAt)

	rec.setStatus(http.StatusOK)
	deliverOnce(t, s, rec)

	sub, err = s.dbSvc.GetSubscriber(t.Context(), s.page.UID, created.UID)
	r.NoError(err)
	r.Zero(sub.FailureCount, "a success must reset the consecutive-failure count")
	r.Nil(sub.DisabledAt)
}

// TestListNeverEchoesTheEndpointURL is the confidentiality property: an
// incoming-webhook URL is a credential, so the admin list must show a mask.
func TestListNeverEchoesTheEndpointURL(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newSubSetup(t)

	const url = "https://hooks.slack.com/services/T0000/B0000/SuperSecretToken"

	_, err := s.svc.CreateEndpointSubscriber(t.Context(), s.org.Slug, s.page.UID,
		&statussubscribers.CreateEndpointSubscriberRequest{Channel: "slack", URL: url})
	r.NoError(err)

	list, err := s.svc.ListSubscribers(t.Context(), s.org.Slug, s.page.UID)
	r.NoError(err)
	r.Len(list, 1)
	r.Equal("slack", list[0].Channel)
	r.NotNil(list[0].Endpoint)
	r.NotContains(*list[0].Endpoint, "SuperSecretToken")
	r.Contains(*list[0].Endpoint, "hooks.slack.com")

	// The stored row must not hold the URL in the clear either.
	sub, err := s.dbSvc.GetSubscriber(t.Context(), s.page.UID, list[0].UID)
	r.NoError(err)
	r.NotNil(sub.EndpointPrivate)
	r.NotNil(sub.EndpointKey)
	r.NotContains(*sub.EndpointKey, "SuperSecretToken")
}

// TestEndpointValidation covers the create-path rules: https only, no
// loopback/private hosts (SSRF), and a Slack row must actually point at Slack.
//
// It builds its OWN strict service — the shared fixture opts into
// WithAllowPrivateEndpoints so the delivery tests can reach an httptest server,
// and a test of the guard must not run with the guard relaxed.
func TestEndpointValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		channel string
		url     string
		wantErr error
	}{
		{"http refused", "webhook", "http://example.com/hook", statussubscribers.ErrInvalidEndpointURL},
		{"localhost refused", "webhook", "https://localhost/hook", statussubscribers.ErrInvalidEndpointURL},
		{"loopback refused", "webhook", "https://127.0.0.1/hook", statussubscribers.ErrInvalidEndpointURL},
		{"private range refused", "webhook", "https://10.1.2.3/hook", statussubscribers.ErrInvalidEndpointURL},
		{"link-local refused", "webhook", "https://169.254.169.254/latest", statussubscribers.ErrInvalidEndpointURL},
		{"empty refused", "webhook", "", statussubscribers.ErrEndpointRequired},
		{"slack must be slack", "slack", "https://example.com/hook", statussubscribers.ErrSlackWebhookExpected},
		{"email channel refused here", "email", "https://example.com/hook", statussubscribers.ErrInvalidChannel},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			s := newSubSetup(t)

			// STRICT service: the shared fixture relaxes the private-host rule
			// so the delivery tests can talk to an httptest server on
			// 127.0.0.1, and this is precisely the test that must not.
			strict := statussubscribers.NewService(s.dbSvc, nil)

			_, err := strict.CreateEndpointSubscriber(t.Context(), s.org.Slug, s.page.UID,
				&statussubscribers.CreateEndpointSubscriberRequest{Channel: tc.channel, URL: tc.url})
			r.ErrorIs(err, tc.wantErr)
		})
	}
}

// TestPublicSubscribeStaysEmailOnly pins the deliberate asymmetry: a visitor
// cannot register a delivery URL, because there is no way to verify they own
// it.
func TestPublicSubscribeStaysEmailOnly(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newSubSetup(t)

	channel := "webhook"
	_, err := s.svc.Subscribe(t.Context(), s.org.Slug, s.page.UID, &statussubscribers.SubscribeRequest{
		Email: "someone@example.com", Channel: &channel,
	})
	r.ErrorIs(err, statussubscribers.ErrPublicChannelNotAllowed)

	// Positive control: the email channel still works from the public path.
	email := "email"
	_, err = s.svc.Subscribe(t.Context(), s.org.Slug, s.page.UID, &statussubscribers.SubscribeRequest{
		Email: "someone@example.com", Channel: &email,
	})
	r.NoError(err)
}

// TestEndpointSubscribersAreBornConfirmed: an operator registering their own
// webhook is the verification, so there is no double opt-in to wait for — and
// the very first publication after registration must deliver.
func TestEndpointSubscribersAreBornConfirmed(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newSubSetup(t)
	rec := newReceiver(t)

	endpointSubscriber(t, s, rec.server.URL+"/hooks/first", "sec")
	deliverOnce(t, s, rec)

	r.Len(rec.captured(), 1)
}

// TestMaskedEndpointDistinguishesTwoWebhooks — the mask has to be useful, not
// just safe: two different webhooks must not render identically in the list.
func TestMaskedEndpointDistinguishesTwoWebhooks(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newSubSetup(t)

	for _, url := range []string{
		"https://hooks.example.com/services/aaaa1111",
		"https://hooks.example.com/services/bbbb2222",
	} {
		_, err := s.svc.CreateEndpointSubscriber(t.Context(), s.org.Slug, s.page.UID,
			&statussubscribers.CreateEndpointSubscriberRequest{Channel: "webhook", URL: url})
		r.NoError(err)
	}

	list, err := s.svc.ListSubscribers(t.Context(), s.org.Slug, s.page.UID)
	r.NoError(err)
	r.Len(list, 2)
	r.NotEqual(*list[0].Endpoint, *list[1].Endpoint)
	r.True(strings.HasPrefix(*list[0].Endpoint, "https://hooks.example.com/"))
}

// TestSigningSecretIsReturnedOnceAndNeverAgain pins the show-once contract.
//
// It matters because the alternative — generating a secret and never revealing
// it — produces signed deliveries the receiver cannot verify, which is worse
// than not signing at all: it looks secure and is not.
func TestSigningSecretIsReturnedOnceAndNeverAgain(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newSubSetup(t)

	created, err := s.svc.CreateEndpointSubscriber(t.Context(), s.org.Slug, s.page.UID,
		&statussubscribers.CreateEndpointSubscriberRequest{
			Channel: "webhook", URL: "https://hooks.example.com/services/abc123",
		})
	r.NoError(err)
	r.NotEmpty(created.SigningSecret, "a generated secret must be handed back once")

	// ...and the list shape has nowhere to put it. Serialize a list row and
	// assert the key is simply absent, rather than trusting the struct by eye.
	list, err := s.svc.ListSubscribers(t.Context(), s.org.Slug, s.page.UID)
	r.NoError(err)
	r.Len(list, 1)

	encoded, err := json.Marshal(list[0])
	r.NoError(err)
	r.NotContains(string(encoded), "signingSecret")
	r.NotContains(string(encoded), created.SigningSecret)
}

// TestSuppliedSigningSecretIsUsedVerbatim: an operator whose receiver already
// verifies a known secret must be able to bring it, or they have to rewrite
// their verifier for this one integration.
func TestSuppliedSigningSecretIsUsedVerbatim(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newSubSetup(t)
	rec := newReceiver(t)

	const mine = "my-own-shared-secret"

	created := endpointSubscriber(t, s, rec.server.URL+"/hooks/mine", mine)
	r.Equal(mine, created.SigningSecret, "a supplied secret must be echoed, not replaced")

	deliverOnce(t, s, rec)

	got := rec.captured()
	r.Len(got, 1)

	digest := sha256.Sum256(got[0].Body)
	canonical := got[0].Timestamp + ".POST." + got[0].Path + "." + hex.EncodeToString(digest[:])
	mac := hmac.New(sha256.New, []byte(mine))
	mac.Write([]byte(canonical))

	r.Equal(servicesig.SignaturePrefix+base64.StdEncoding.EncodeToString(mac.Sum(nil)), got[0].Signature,
		"the delivery must be signed with the operator's own secret")
}
