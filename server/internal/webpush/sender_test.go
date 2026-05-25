package webpush_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	webpushgo "github.com/SherClockHolmes/webpush-go"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/webpush"
)

// realVAPIDKeys generates a throwaway VAPID keypair for tests.
func realVAPIDKeys(t *testing.T) (pub, priv string) {
	t.Helper()

	priv, pub, err := webpushgo.GenerateVAPIDKeys()
	require.NoError(t, err)

	return pub, priv
}

// fakeSubscription returns a minimal JSON push subscription that points to the
// given test server URL. The keys are fake but structurally valid.
func fakeSubscription(t *testing.T, serverURL string) string {
	t.Helper()

	sub := map[string]any{
		"endpoint": serverURL + "/push",
		"keys": map[string]string{
			// These are valid-length base64url strings that satisfy the webpush-go parser.
			"p256dh": "BNcR2oRRFkqEBMXPEBfHhpZCZlDpv3MRF3E9pkBj2_RDZLLJl4iyq-MkzFwOeOj4-FHLS8eJv9BxGJnDdz4vQ0",
			"auth":   "tBHItnDR9oNFbwKMkhY7lQ",
		},
	}
	b, err := json.Marshal(sub)
	require.NoError(t, err)

	return string(b)
}

func TestSend_Success(t *testing.T) {
	t.Parallel()

	pub, priv := realVAPIDKeys(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify VAPID Authorization header is present.
		require.NotEmpty(t, r.Header.Get("Authorization"), "Authorization header must be set")
		require.Equal(t, "application/octet-stream", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	opts := webpush.Options{
		VAPIDPublicKey:  pub,
		VAPIDPrivateKey: priv,
		Subject:         "mailto:test@example.com",
	}

	msg := webpush.Message{Title: "Test", Body: "body", URL: "https://example.com"}
	err := webpush.Send(context.Background(), opts, fakeSubscription(t, srv.URL), msg)
	require.NoError(t, err)
}

func TestSend_Gone410(t *testing.T) {
	t.Parallel()

	pub, priv := realVAPIDKeys(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()

	opts := webpush.Options{
		VAPIDPublicKey:  pub,
		VAPIDPrivateKey: priv,
		Subject:         "mailto:test@example.com",
	}

	msg := webpush.Message{Title: "Test", Body: "body"}
	err := webpush.Send(context.Background(), opts, fakeSubscription(t, srv.URL), msg)
	require.ErrorIs(t, err, webpush.ErrSubscriptionGone)
}

func TestSend_NotFound404(t *testing.T) {
	t.Parallel()

	pub, priv := realVAPIDKeys(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	opts := webpush.Options{
		VAPIDPublicKey:  pub,
		VAPIDPrivateKey: priv,
		Subject:         "mailto:test@example.com",
	}

	msg := webpush.Message{Title: "Test", Body: "body"}
	err := webpush.Send(context.Background(), opts, fakeSubscription(t, srv.URL), msg)
	require.ErrorIs(t, err, webpush.ErrSubscriptionGone)
}

func TestSend_ServerError500(t *testing.T) {
	t.Parallel()

	pub, priv := realVAPIDKeys(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	opts := webpush.Options{
		VAPIDPublicKey:  pub,
		VAPIDPrivateKey: priv,
		Subject:         "mailto:test@example.com",
	}

	msg := webpush.Message{Title: "Test", Body: "body"}
	err := webpush.Send(context.Background(), opts, fakeSubscription(t, srv.URL), msg)
	require.Error(t, err)
	require.NotErrorIs(t, err, webpush.ErrSubscriptionGone)
}
