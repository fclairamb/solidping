package usernotifications

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	webpushgo "github.com/SherClockHolmes/webpush-go"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

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
