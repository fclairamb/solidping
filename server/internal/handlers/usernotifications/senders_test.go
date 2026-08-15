package usernotifications

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	webpushgo "github.com/SherClockHolmes/webpush-go"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// capturingSender is a fake email.Sender that records the last message
// instead of dialing SMTP, so EmailSenderAdapter's rendering can be asserted
// directly.
type capturingSender struct {
	last *email.Message
}

func (c *capturingSender) Send(_ context.Context, msg *email.Message) (*email.SendResult, error) {
	c.last = msg

	return &email.SendResult{Sent: true, MessageID: "test-msg-id"}, nil
}

// TestEmailSenderAdapter_SendTestEmail_RendersThroughFormatter proves the
// user-notification test-send email (usernotifications senders.go, site 4 of
// the email-formatter migration) renders through test-email.html — including
// getting an HTML part, which the old hand-rolled version never had at all.
// The negative control matters: a test that only checks "non-empty" would
// pass against the old text-only code too.
func TestEmailSenderAdapter_SendTestEmail_RendersThroughFormatter(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	sender := &capturingSender{}
	adapter := NewEmailSenderAdapter(sender, formatter)

	err = adapter.SendTestEmail(context.Background(), "someone@example.com")
	r.NoError(err)

	r.NotNil(sender.last)
	msg := sender.last

	r.Equal([]string{"someone@example.com"}, msg.Recipients.To)
	r.Equal("Test notification from SolidPing", msg.Subject)
	r.NotEmpty(msg.HTML, "must have an HTML part — the old version was text-only")
	r.NotEmpty(msg.Text, "must have a text part")
	r.Contains(msg.HTML, "Test notification from SolidPing")
	r.Contains(msg.HTML, "delivery is working correctly")

	// Negative control: the pre-migration code never set msg.HTML at all
	// (Text-only), and never wrapped anything in the shared base.html
	// branding. Confirm this is the real template, not a fragment.
	r.Contains(msg.HTML, "SolidPing</span>", "must include the shared base.html header branding")
}

// TestEmailSenderAdapter_SendTestEmail_NilFormatterGuard proves the
// nil-formatter guard added when SendTestEmail was migrated to the shared
// formatter is real, not just assumed.
func TestEmailSenderAdapter_SendTestEmail_NilFormatterGuard(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	adapter := NewEmailSenderAdapter(&capturingSender{}, nil) // formatter never wired

	err := adapter.SendTestEmail(context.Background(), "someone@example.com")
	r.ErrorIs(err, ErrEmailFormatterNotConfigured)
}

// sendersTestVAPIDKeys generates a throwaway VAPID keypair.
func sendersTestVAPIDKeys(t *testing.T) (string, string) {
	t.Helper()

	priv, pub, err := webpushgo.GenerateVAPIDKeys()
	require.NoError(t, err)

	return pub, priv
}

// sendersTestSubJSON builds a minimal PushSubscription JSON pointing to the
// given server URL.
func sendersTestSubJSON(t *testing.T, serverURL string) string {
	t.Helper()

	p256dh := "BLiMDpdL9AFok4VWdDMMek7hFVBaleGPi8DVegkLJFH7IFnJo5zAP1GjH50H-njZFrZJ1etQf3F38z68FzSPa1Y"

	return `{"endpoint":"` + serverURL + `/push",` +
		`"keys":{"p256dh":"` + p256dh + `","auth":"tBHItnDR9oNFbwKMkhY7lQ"}}`
}

// TestDispatchTestRoute_WebPush_Success verifies that dispatchTestRoute sends a
// web push request to the mock push service when VAPID keys are configured.
func TestDispatchTestRoute_WebPush_Success(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	requestReceived := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestReceived = true
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	pub, priv := sendersTestVAPIDKeys(t)

	wpOpts := webpush.Options{
		VAPIDPublicKey:  pub,
		VAPIDPrivateKey: priv,
		Subject:         "mailto:test@example.com",
	}

	contactValue := sendersTestSubJSON(t, srv.URL)

	svc := &Service{} // db not needed for dispatchTestRoute with webpush

	route := &models.UserNotificationRoute{
		UID:     "route-1",
		UserUID: "user-1",
		Contact: &models.UserContact{
			UID:   "contact-1",
			Type:  models.UserContactTypeWebPush,
			Value: contactValue,
		},
	}

	err := svc.dispatchTestRoute(context.Background(), "org-1", "org-one", route, nil, nil, wpOpts)
	r.NoError(err)
	r.True(requestReceived, "push service must receive the test request")
}

// TestDispatchTestRoute_WebPush_NotConfigured verifies that dispatchTestRoute
// returns ErrWebPushNotConfigured when VAPID keys are absent.
func TestDispatchTestRoute_WebPush_NotConfigured(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	svc := &Service{}

	route := &models.UserNotificationRoute{
		UID:     "route-1",
		UserUID: "user-1",
		Contact: &models.UserContact{
			UID:   "contact-1",
			Type:  models.UserContactTypeWebPush,
			Value: `{"endpoint":"https://example.com","keys":{"p256dh":"abc","auth":"def"}}`,
		},
	}

	err := svc.dispatchTestRoute(context.Background(), "org-1", "org-one", route, nil, nil, webpush.Options{})
	r.ErrorIs(err, ErrWebPushNotConfigured)
}
