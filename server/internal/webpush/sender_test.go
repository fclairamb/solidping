package webpush_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	webpushgo "github.com/SherClockHolmes/webpush-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/webpush"
)

// realVAPIDKeys generates a throwaway VAPID keypair for tests.
func realVAPIDKeys(t *testing.T) (string, string) {
	t.Helper()

	priv, pub, err := webpushgo.GenerateVAPIDKeys()
	require.NoError(t, err)

	return pub, priv
}

// fakeSubscription returns a minimal JSON push subscription that points to the
// given test server URL. The p256dh value is a valid P-256 uncompressed public
// key (65 bytes, base64url-encoded) generated once for test use.
func fakeSubscription(t *testing.T, serverURL string) string {
	t.Helper()

	sub := map[string]any{
		"endpoint": serverURL + "/push",
		"keys": map[string]string{
			// Valid P-256 uncompressed public key (65 bytes → 87 base64url chars).
			"p256dh": "BLiMDpdL9AFok4VWdDMMek7hFVBaleGPi8DVegkLJFH7IFnJo5zAP1GjH50H-njZFrZJ1etQf3F38z68FzSPa1Y",
			// 16-byte base64url auth secret.
			"auth": "tBHItnDR9oNFbwKMkhY7lQ",
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
		// Verify VAPID Authorization header is present (use assert, not require in handler).
		assert.NotEmpty(t, r.Header.Get("Authorization"), "Authorization header must be set")
		assert.Equal(t, "application/octet-stream", r.Header.Get("Content-Type"))
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
