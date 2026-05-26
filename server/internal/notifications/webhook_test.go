package notifications

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// TestSignRequest_KnownVector pins the signature for a fixed secret + id +
// timestamp + body against the Standard Webhooks reference algorithm. The
// expected value is computed independently below to guard against regressions
// in the signing routine (prefix stripping, base64url-decode, HMAC, base64
// std-encode).
func TestSignRequest_KnownVector(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// The Standard Webhooks docs use this secret in their examples.
	secret := "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	id := "msg_p5jXN8AQM9LWM0D4loKWxJek"
	timestamp := "1614265330"
	body := []byte(`{"test": 2432232314}`)

	header, err := signRequest([]string{secret}, id, timestamp, body)
	r.NoError(err)

	// Single secret → exactly one "v1,<sig>" entry, no spaces.
	r.True(strings.HasPrefix(header, "v1,"))
	r.NotContains(header, " ")

	// This is the signature published in the Standard Webhooks verification
	// docs for the above inputs.
	r.Equal("v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=", header)
}

// TestSignRequest_TwoSecrets verifies the rotation case: two secrets produce
// two space-separated "v1,…" entries, both verifiable.
func TestSignRequest_TwoSecrets(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	s1, err := generateWebhookSecret()
	r.NoError(err)
	s2, err := generateWebhookSecret()
	r.NoError(err)

	header, err := signRequest([]string{s1, s2}, "id-1", "1700000000", []byte(`{}`))
	r.NoError(err)

	parts := strings.Split(header, " ")
	r.Len(parts, 2)
	for _, p := range parts {
		r.True(strings.HasPrefix(p, "v1,"), "each entry must be v1-prefixed: %q", p)
	}
	r.NotEqual(parts[0], parts[1], "different secrets must yield different signatures")
}

// TestSignRequest_InvalidSecret rejects a secret whose body isn't base64url.
func TestSignRequest_InvalidSecret(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, err := signRequest([]string{"whsec_not base64!!"}, "id", "1", []byte(`{}`))
	r.ErrorIs(err, ErrInvalidSigningSecret)
}

func testPayload(connSettings models.JSONMap) *Payload {
	name := "API health"
	title := "api.example.com -> HTTP 500"

	return &Payload{
		EventType: "incident.created",
		Incident: &models.Incident{
			UID:          "018e4a2b-incident",
			StartedAt:    time.Now().Add(-2 * time.Minute),
			FailureCount: 3,
			Title:        &title,
		},
		Check: &models.Check{
			UID:  "018e4a1c-check",
			Name: &name,
			Type: "http",
		},
		Connection: &models.Channel{
			UID:             "chan-1",
			OrganizationUID: "org-1",
			Type:            models.ConnectionTypeWebhook,
			Settings:        connSettings,
		},
	}
}

func newJobCtx() *jobdef.JobContext {
	return &jobdef.JobContext{Logger: slog.Default()}
}

// TestWebhookSender_Send_SetsHeaders asserts the three Standard Webhooks
// headers plus Content-Type are present and well-formed.
func TestWebhookSender_Send_SetsHeaders(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotHeaders = req.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	secret, err := generateWebhookSecret()
	r.NoError(err)

	payload := testPayload(models.JSONMap{
		"url":                    srv.URL,
		settingsKeySigningSecret: secret,
	})

	sender := &WebhookSender{}
	r.NoError(sender.Send(context.Background(), newJobCtx(), payload))

	id := gotHeaders.Get(headerWebhookID)
	r.NotEmpty(id)
	r.Len(strings.Split(id, "-"), 5, "webhook-id should be a UUID")

	ts := gotHeaders.Get(headerWebhookTimestamp)
	r.NotEmpty(ts)
	_, convErr := strconv.ParseInt(ts, 10, 64)
	r.NoError(convErr, "webhook-timestamp must be numeric")

	sig := gotHeaders.Get(headerWebhookSignature)
	r.True(strings.HasPrefix(sig, "v1,"))
	r.Equal("application/json", gotHeaders.Get("Content-Type"))
	r.Equal("SolidPing/1.0", gotHeaders.Get("User-Agent"))

	// The webhook-id is exposed back on the payload for auditing.
	r.Equal(id, payload.MessageID)
}

// TestWebhookSender_Send_PayloadShape asserts the typed envelope.
func TestWebhookSender_Send_PayloadShape(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotBody, _ = io.ReadAll(req.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	secret, err := generateWebhookSecret()
	r.NoError(err)

	payload := testPayload(models.JSONMap{
		"url":                    srv.URL,
		settingsKeySigningSecret: secret,
	})

	sender := &WebhookSender{}
	r.NoError(sender.Send(context.Background(), newJobCtx(), payload))

	var env WebhookPayload
	r.NoError(json.Unmarshal(gotBody, &env))
	r.Equal("incident.created", env.Type)
	r.False(env.Timestamp.IsZero())
	r.Equal("018e4a2b-incident", env.Data.Incident.UID)
	r.Equal("API health", env.Data.Check.Name)
	r.Equal("http", env.Data.Check.Type)
	r.Equal(3, env.Data.Incident.FailureCount)
	// No relapse → RecoveryPeriodSeconds omitted (null).
	r.Nil(env.Data.Incident.RecoveryPeriodSeconds)
	// Not resolved → durationSeconds null.
	r.Nil(env.Data.Incident.DurationSeconds)
}

// TestWebhookSender_Send_CustomHeadersCannotOverrideSigning ensures custom
// headers may set User-Agent but never the three signing headers.
func TestWebhookSender_Send_CustomHeadersCannotOverrideSigning(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got = req.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	secret, err := generateWebhookSecret()
	r.NoError(err)

	payload := testPayload(models.JSONMap{
		"url":                    srv.URL,
		settingsKeySigningSecret: secret,
		"headers": map[string]any{
			"User-Agent":        "Custom/9.9",
			"webhook-signature": "v1,attacker",
			"X-Extra":           "yes",
		},
	})

	sender := &WebhookSender{}
	r.NoError(sender.Send(context.Background(), newJobCtx(), payload))

	r.Equal("Custom/9.9", got.Get("User-Agent"), "custom header may override User-Agent")
	r.Equal("yes", got.Get("X-Extra"))
	r.NotEqual("v1,attacker", got.Get(headerWebhookSignature), "signing header must not be overridable")
	r.True(strings.HasPrefix(got.Get(headerWebhookSignature), "v1,"))
}

// TestWebhookSender_Send_AutoGeneratesSecret verifies the UpdateChannel
// callback fires when no secret exists, and the request still carries a valid
// signature computed from the freshly-generated secret.
func TestWebhookSender_Send_AutoGeneratesSecret(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got = req.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	payload := testPayload(models.JSONMap{"url": srv.URL})

	called := false
	sender := &WebhookSender{
		UpdateChannel: func(_ context.Context, ch *models.Channel) error {
			called = true
			secret, ok := ch.Settings[settingsKeySigningSecret].(string)
			r.True(ok)
			r.True(strings.HasPrefix(secret, webhookSecretPrefix))

			return nil
		},
	}

	r.NoError(sender.Send(context.Background(), newJobCtx(), payload))
	r.True(called, "UpdateChannel must be invoked to persist the generated secret")
	r.True(strings.HasPrefix(got.Get(headerWebhookSignature), "v1,"))

	// The channel's in-memory settings now carry the generated secret.
	secret, ok := payload.Connection.Settings[settingsKeySigningSecret].(string)
	r.True(ok)
	r.True(strings.HasPrefix(secret, webhookSecretPrefix))
}

// TestWebhookSender_Send_NoUpdateCallbackWarns ensures auto-generation works
// without a persistence callback (Send still signs, just doesn't persist).
func TestWebhookSender_Send_NoUpdateCallbackWarns(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got = req.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	payload := testPayload(models.JSONMap{"url": srv.URL})

	sender := &WebhookSender{} // no UpdateChannel
	r.NoError(sender.Send(context.Background(), newJobCtx(), payload))
	r.True(strings.HasPrefix(got.Get(headerWebhookSignature), "v1,"))
}

// TestWebhookSender_Send_TwoActiveSecrets verifies a rotation-in-progress
// channel signs with two secrets.
func TestWebhookSender_Send_TwoActiveSecrets(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got = req.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	active, err := generateWebhookSecret()
	r.NoError(err)
	previous, err := generateWebhookSecret()
	r.NoError(err)

	payload := testPayload(models.JSONMap{
		"url":                            srv.URL,
		settingsKeySigningSecret:         active,
		settingsKeySigningSecretPrevious: previous,
		settingsKeySigningSecretExpiry:   time.Now().Add(time.Hour).Format(time.RFC3339),
	})

	sender := &WebhookSender{}
	r.NoError(sender.Send(context.Background(), newJobCtx(), payload))

	parts := strings.Split(got.Get(headerWebhookSignature), " ")
	r.Len(parts, 2, "both active and previous secret must sign")
}

// TestWebhookSender_Send_ExpiredPreviousSecretPurged verifies that an expired
// previous secret is dropped (only one signature) and UpdateChannel is invoked
// to persist the purge.
func TestWebhookSender_Send_ExpiredPreviousSecretPurged(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got = req.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	active, err := generateWebhookSecret()
	r.NoError(err)
	previous, err := generateWebhookSecret()
	r.NoError(err)

	payload := testPayload(models.JSONMap{
		"url":                            srv.URL,
		settingsKeySigningSecret:         active,
		settingsKeySigningSecretPrevious: previous,
		settingsKeySigningSecretExpiry:   time.Now().Add(-time.Hour).Format(time.RFC3339),
	})

	purged := false
	sender := &WebhookSender{
		UpdateChannel: func(_ context.Context, ch *models.Channel) error {
			purged = true
			_, hasPrev := ch.Settings[settingsKeySigningSecretPrevious]
			_, hasExp := ch.Settings[settingsKeySigningSecretExpiry]
			r.False(hasPrev, "previous secret must be cleared")
			r.False(hasExp, "previous expiry must be cleared")

			return nil
		},
	}

	r.NoError(sender.Send(context.Background(), newJobCtx(), payload))
	r.True(purged, "UpdateChannel must be invoked to persist the purge")

	parts := strings.Split(got.Get(headerWebhookSignature), " ")
	r.Len(parts, 1, "only the active secret should remain after purge")
}

// TestWebhookSender_Send_MissingURL fails fast when no URL is configured.
func TestWebhookSender_Send_MissingURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	payload := testPayload(models.JSONMap{})
	sender := &WebhookSender{}
	err := sender.Send(context.Background(), newJobCtx(), payload)
	r.ErrorIs(err, ErrWebhookURLNotConfigured)
}

// TestWebhookSender_URLKeyResolution verifies that Send succeeds when
// Settings["url"] is set, and returns ErrWebhookURLNotConfigured when the
// key is absent. This guards the contract between the frontend (which writes
// "url" for the generic webhook type) and the backend sender (which reads "url").
func TestWebhookSender_URLKeyResolution(t *testing.T) {
	t.Parallel()

	// Sub-test: Send succeeds when Settings["url"] is populated.
	t.Run("url_key_present", func(t *testing.T) {
		t.Parallel()

		tr := require.New(t)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		payload := testPayload(models.JSONMap{"url": srv.URL})
		sender := &WebhookSender{}
		tr.NoError(sender.Send(context.Background(), newJobCtx(), payload))
	})

	// Sub-test: Send returns ErrWebhookURLNotConfigured when no URL key is present.
	t.Run("url_key_absent", func(t *testing.T) {
		t.Parallel()

		tr := require.New(t)

		payload := testPayload(models.JSONMap{})
		sender := &WebhookSender{}
		err := sender.Send(context.Background(), newJobCtx(), payload)
		tr.ErrorIs(err, ErrWebhookURLNotConfigured)
	})
}

// TestWebhookSender_Send_RelapseIncludesRecoveryPeriod verifies the recovery
// period is included once a relapse has occurred.
func TestWebhookSender_Send_RelapseIncludesRecoveryPeriod(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotBody, _ = io.ReadAll(req.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	secret, err := generateWebhookSecret()
	r.NoError(err)

	payload := testPayload(models.JSONMap{
		"url":                    srv.URL,
		settingsKeySigningSecret: secret,
	})
	payload.Incident.RelapseCount = 2
	payload.Check.RecoveryPeriodSeconds = 300

	sender := &WebhookSender{}
	r.NoError(sender.Send(context.Background(), newJobCtx(), payload))

	var env WebhookPayload
	r.NoError(json.Unmarshal(gotBody, &env))
	r.NotNil(env.Data.Incident.RecoveryPeriodSeconds)
	r.Equal(300, *env.Data.Incident.RecoveryPeriodSeconds)
	r.Equal(2, env.Data.Incident.RelapseCount)
}
