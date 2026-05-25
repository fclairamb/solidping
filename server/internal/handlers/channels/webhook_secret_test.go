package channels_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/channels"
)

const (
	secretKey         = "signingSecret"
	secretPreviousKey = "signingSecretPrevious"
	secretExpiryKey   = "signingSecretPreviousExpiry"
)

func newWebhookTestSvc(t *testing.T) (*channels.Service, *models.Organization, context.Context) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	slug := "wh" + sanitizeSlug(t.Name())
	org := models.NewOrganization(slug, "Webhook Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return channels.NewService(dbSvc, creds), org, ctx
}

// sanitizeSlug derives a valid org slug (lowercase alphanumeric, capped length)
// from a test name so each test gets an isolated organization.
func sanitizeSlug(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		}
	}

	out := b.String()
	if len(out) > 16 {
		out = out[:16]
	}

	return out
}

// TestCreateWebhookChannel_GeneratesSecret asserts a webhook channel gets a
// non-empty `whsec_`-prefixed signing secret on creation, retrievable on GET.
func TestCreateWebhookChannel_GeneratesSecret(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, org, ctx := newWebhookTestSvc(t)

	created, err := svc.CreateChannel(ctx, org.Slug, channels.CreateChannelRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"url": "https://example.com/hook"},
	})
	r.NoError(err)

	secret, ok := created.Settings[secretKey].(string)
	r.True(ok, "create response must surface the signing secret")
	r.True(strings.HasPrefix(secret, "whsec_"))

	// GET must also surface the (decrypted) secret, and it must not appear in
	// the placeholder-only SettingsPrivateKeys list.
	got, err := svc.GetChannel(ctx, org.Slug, created.UID)
	r.NoError(err)
	gotSecret, ok := got.Settings[secretKey].(string)
	r.True(ok)
	r.Equal(secret, gotSecret)
	r.NotContains(got.SettingsPrivateKeys, secretKey)
}

// TestRotateWebhookSecret_CyclesSecrets verifies the old secret moves to
// previous, a fresh secret is generated, and the expiry is ~24 h out.
func TestRotateWebhookSecret_CyclesSecrets(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, org, ctx := newWebhookTestSvc(t)

	created, err := svc.CreateChannel(ctx, org.Slug, channels.CreateChannelRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"url": "https://example.com/hook"},
	})
	r.NoError(err)
	original := created.Settings[secretKey].(string)

	rotated, err := svc.RotateWebhookSecret(ctx, org.Slug, created.UID)
	r.NoError(err)

	newSecret := rotated.Settings[secretKey].(string)
	r.NotEqual(original, newSecret, "rotation must produce a fresh secret")
	r.True(strings.HasPrefix(newSecret, "whsec_"))

	prev, ok := rotated.Settings[secretPreviousKey].(string)
	r.True(ok, "previous secret must be retained during the grace window")
	r.Equal(original, prev)

	expiryRaw, ok := rotated.Settings[secretExpiryKey].(string)
	r.True(ok)
	expiry, parseErr := time.Parse(time.RFC3339, expiryRaw)
	r.NoError(parseErr)
	r.WithinDuration(time.Now().Add(24*time.Hour), expiry, 2*time.Minute)
}

// TestRotateWebhookSecret_WrongType rejects rotation on non-webhook channels.
func TestRotateWebhookSecret_WrongType(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, org, ctx := newWebhookTestSvc(t)

	created, err := svc.CreateChannel(ctx, org.Slug, channels.CreateChannelRequest{
		Type:     "discord",
		Name:     "disc",
		Settings: map[string]any{"webhook_url": "https://discord.example/hook"},
	})
	r.NoError(err)

	_, err = svc.RotateWebhookSecret(ctx, org.Slug, created.UID)
	r.ErrorIs(err, channels.ErrNotWebhookChannel)
}

// TestTestWebhookChannel_Success delivers to a 200 endpoint and asserts the
// result fields, including the signing headers seen server-side.
func TestTestWebhookChannel_Success(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, org, ctx := newWebhookTestSvc(t)

	var gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotSig = req.Header.Get("webhook-signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	created, err := svc.CreateChannel(ctx, org.Slug, channels.CreateChannelRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"url": srv.URL},
	})
	r.NoError(err)

	result, err := svc.TestWebhookChannel(ctx, org.Slug, created.UID)
	r.NoError(err)
	r.True(result.Success)
	r.Equal(200, result.StatusCode)
	r.GreaterOrEqual(result.DurationMs, int64(0))
	r.Empty(result.Error)
	r.True(strings.HasPrefix(gotSig, "v1,"), "test delivery must be signed")
}

// TestTestWebhookChannel_Failure reports a non-2xx as success=false with the
// status code surfaced.
func TestTestWebhookChannel_Failure(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, org, ctx := newWebhookTestSvc(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	created, err := svc.CreateChannel(ctx, org.Slug, channels.CreateChannelRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"url": srv.URL},
	})
	r.NoError(err)

	result, err := svc.TestWebhookChannel(ctx, org.Slug, created.UID)
	r.NoError(err, "test endpoint never returns an error for a remote failure")
	r.False(result.Success)
	r.Equal(500, result.StatusCode)
	r.NotEmpty(result.Error)
}

// TestTestWebhookChannel_WrongType rejects testing a non-webhook channel.
func TestTestWebhookChannel_WrongType(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, org, ctx := newWebhookTestSvc(t)

	created, err := svc.CreateChannel(ctx, org.Slug, channels.CreateChannelRequest{
		Type:     "discord",
		Name:     "disc",
		Settings: map[string]any{"webhook_url": "https://discord.example/hook"},
	})
	r.NoError(err)

	_, err = svc.TestWebhookChannel(ctx, org.Slug, created.UID)
	r.ErrorIs(err, channels.ErrNotWebhookChannel)
}
