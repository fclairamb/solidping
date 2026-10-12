package integrations_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
)

const (
	secretKey         = "signingSecret"
	secretPreviousKey = "signingSecretPrevious"
	secretExpiryKey   = "signingSecretPreviousExpiry"
)

func newWebhookTestSvc(t *testing.T) (*integrations.Service, *models.Organization, context.Context) {
	t.Helper()

	env := newWebhookTestEnv(t)

	return env.svc, env.org, env.ctx
}

// webhookTestEnv exposes the storage behind the service so a test can plant a
// legacy stored secret and read storage back without going through the API.
type webhookTestEnv struct {
	svc   *integrations.Service
	org   *models.Organization
	ctx   context.Context
	db    *sqlite.Service
	creds credentials.Service
}

func newWebhookTestEnv(t *testing.T) *webhookTestEnv {
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

	return &webhookTestEnv{
		svc:   integrations.NewService(dbSvc, creds, nil, nil),
		org:   org,
		ctx:   ctx,
		db:    dbSvc,
		creds: creds,
	}
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

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"url": "https://example.com/hook"},
	})
	r.NoError(err)

	secret, ok := created.Settings[secretKey].(string)
	r.True(ok, "create response must surface the signing secret")
	requireStandardSecret(r, secret)

	// GET must also surface the (decrypted) secret, and it must not appear in
	// the placeholder-only SettingsPrivateKeys list.
	got, err := svc.GetIntegration(ctx, org.Slug, created.UID)
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

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"url": "https://example.com/hook"},
	})
	r.NoError(err)
	original, ok := created.Settings[secretKey].(string)
	r.True(ok)

	rotated, err := svc.RotateWebhookSecret(ctx, org.Slug, created.UID)
	r.NoError(err)

	newSecret, ok := rotated.Settings[secretKey].(string)
	r.True(ok)
	r.NotEqual(original, newSecret, "rotation must produce a fresh secret")
	requireStandardSecret(r, newSecret)

	prev, ok := rotated.Settings[secretPreviousKey].(string)
	r.True(ok, "previous secret must be retained during the grace window")
	r.Equal(original, prev)

	expiryRaw, ok := rotated.Settings[secretExpiryKey].(string)
	r.True(ok)
	expiry, parseErr := time.Parse(time.RFC3339, expiryRaw)
	r.NoError(parseErr)
	r.WithinDuration(time.Now().Add(24*time.Hour), expiry, 2*time.Minute)
}

// TestRotateWebhookSecret_WrongType rejects rotation on non-webhook integrations.
func TestRotateWebhookSecret_WrongType(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, org, ctx := newWebhookTestSvc(t)

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type:     "discord",
		Name:     "disc",
		Settings: map[string]any{"webhook_url": "https://discord.com/api/webhooks/1/acme"},
	})
	r.NoError(err)

	_, err = svc.RotateWebhookSecret(ctx, org.Slug, created.UID)
	r.ErrorIs(err, integrations.ErrNotWebhookChannel)
}

// TestTestWebhookChannel_Success delivers to a 200 endpoint and asserts the
// result fields, including the signing headers seen server-side.
func TestTestWebhookChannel_Success(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, org, ctx := newWebhookTestSvc(t)

	var gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotSig = req.Header.Get("Webhook-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"url": srv.URL},
	})
	r.NoError(err)

	result, err := svc.TestIntegration(ctx, org.Slug, created.UID)
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

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: map[string]any{"url": srv.URL},
	})
	r.NoError(err)

	result, err := svc.TestIntegration(ctx, org.Slug, created.UID)
	r.NoError(err, "test endpoint never returns an error for a remote failure")
	r.False(result.Success)
	r.Equal(500, result.StatusCode)
	r.NotEmpty(result.Error)
}

// TestTestIntegration_NotNotifiable rejects testing a data-source-only
// integration (e.g. Freebox), which has no sender to deliver through.
func TestTestIntegration_NotNotifiable(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, org, ctx := newWebhookTestSvc(t)

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type: "freebox",
		Name: "fbx",
	})
	r.NoError(err)

	_, err = svc.TestIntegration(ctx, org.Slug, created.UID)
	r.ErrorIs(err, integrations.ErrIntegrationNotNotifiable)
}

var standardSecretRE = regexp.MustCompile(`^whsec_[A-Za-z0-9+/]{43}=$`)

// legacyKey spells with both '-' and '_' in URL-safe base64, and with '+' and
// '/' in standard base64, so the two spellings never coincide.
//
//nolint:gochecknoglobals // fixed test vector
var legacyKey = []byte{
	0xfb, 0xff, 0xbf, 0xfe, 0x00, 0x01, 0x02, 0x03, 0xfb, 0xef, 0xbe, 0xfb,
	0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80, 0x90, 0xa0, 0xb0, 0xc0,
	0xd0, 0xe0, 0xf0, 0x01, 0x11, 0x21, 0x31, 0x41,
}

func legacySecret(key []byte) string {
	return "whsec_" + base64.RawURLEncoding.EncodeToString(key)
}

func standardSecret(key []byte) string {
	return "whsec_" + base64.StdEncoding.EncodeToString(key)
}

// requireStandardSecret asserts a generated secret is `whsec_` + standard
// padded base64 of 32 bytes.
func requireStandardSecret(r *require.Assertions, secret string) {
	r.Regexp(standardSecretRE, secret)

	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	r.NoError(err)
	r.Len(key, 32)
}

// plantStoredSecrets writes signing secrets straight to storage, bypassing the
// service's canonicalization, the way a pre-upgrade row looks.
func plantStoredSecrets(t *testing.T, env *webhookTestEnv, uid string, extra map[string]any) {
	t.Helper()

	r := require.New(t)

	conn, err := env.db.GetChannel(env.ctx, uid)
	r.NoError(err)

	effective, err := credentials.OpenConnectionSettings(env.ctx, env.creds, conn)
	r.NoError(err)

	for k, v := range extra {
		effective[k] = v
	}

	sealed, err := credentials.SealConnectionSettings(env.ctx, env.creds, conn.Type, conn.OrganizationUID, effective)
	r.NoError(err)

	settings := models.JSONMap(sealed.Public)
	r.NoError(env.db.UpdateChannel(env.ctx, uid, &models.IntegrationUpdate{
		Settings:             &settings,
		SettingsPrivate:      sealed.Private,
		SettingsPrivateKeys:  sealed.PrivateKeys,
		ClearSettingsPrivate: sealed.Private == nil,
	}))
}

// storedSettings returns the decrypted settings as stored, not as the API
// renders them.
func storedSettings(t *testing.T, env *webhookTestEnv, uid string) map[string]any {
	t.Helper()

	r := require.New(t)

	conn, err := env.db.GetChannel(env.ctx, uid)
	r.NoError(err)

	effective, err := credentials.OpenConnectionSettings(env.ctx, env.creds, conn)
	r.NoError(err)

	return effective
}

func createWebhook(t *testing.T, env *webhookTestEnv, settings map[string]any) (*integrations.IntegrationResponse, error) {
	t.Helper()

	base := map[string]any{"url": "https://example.com/hook"}
	for k, v := range settings {
		base[k] = v
	}

	return env.svc.CreateIntegration(env.ctx, env.org.Slug, integrations.CreateIntegrationRequest{
		Type:     "webhook",
		Name:     "hook",
		Settings: base,
	})
}

func TestCreateWebhookChannel_SuppliedSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		secret  string
		want    string
		wantErr bool
	}{
		{name: "standard padded stored as is", secret: standardSecret(legacyKey), want: standardSecret(legacyKey)},
		{name: "url-safe unpadded stored canonical", secret: legacySecret(legacyKey), want: standardSecret(legacyKey)},
		{name: "missing prefix stored canonical", secret: base64.RawURLEncoding.EncodeToString(legacyKey), want: standardSecret(legacyKey)},
		{name: "garbage rejected", secret: "whsec_not base64!!", wantErr: true},
		{name: "too short rejected", secret: standardSecret(make([]byte, 16)), wantErr: true},
		{name: "too long rejected", secret: standardSecret(make([]byte, 65)), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			env := newWebhookTestEnv(t)

			created, err := createWebhook(t, env, map[string]any{"signingSecret": tc.secret})
			if tc.wantErr {
				r.ErrorIs(err, integrations.ErrInvalidSettings)

				list, listErr := env.svc.ListIntegrations(env.ctx, env.org.Slug, nil)
				r.NoError(listErr)
				r.Empty(list.Data, "a rejected create must not leave a channel behind")

				return
			}

			r.NoError(err)
			r.Equal(tc.want, created.Settings[secretKey])
			r.Equal(tc.want, storedSettings(t, env, created.UID)[secretKey], "storage holds the canonical spelling")
		})
	}
}

func TestUpdateWebhookChannel_SigningSecret(t *testing.T) {
	t.Parallel()

	t.Run("url-safe value stored canonical", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		env := newWebhookTestEnv(t)
		created, err := createWebhook(t, env, nil)
		r.NoError(err)

		_, err = env.svc.UpdateIntegration(env.ctx, env.org.Slug, created.UID, integrations.UpdateIntegrationRequest{
			Settings: map[string]any{"url": "https://example.com/hook", secretKey: legacySecret(legacyKey)},
		})
		r.NoError(err)
		r.Equal(standardSecret(legacyKey), storedSettings(t, env, created.UID)[secretKey])
	})

	t.Run("garbage rejected and stored secret unchanged", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		env := newWebhookTestEnv(t)
		created, err := createWebhook(t, env, nil)
		r.NoError(err)
		before := storedSettings(t, env, created.UID)[secretKey]

		_, err = env.svc.UpdateIntegration(env.ctx, env.org.Slug, created.UID, integrations.UpdateIntegrationRequest{
			Settings: map[string]any{"url": "https://example.com/hook", secretKey: "whsec_not base64!!"},
		})
		r.ErrorIs(err, integrations.ErrInvalidSettings)
		r.Equal(before, storedSettings(t, env, created.UID)[secretKey])
	})

	t.Run("garbage previous secret rejected", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		env := newWebhookTestEnv(t)
		created, err := createWebhook(t, env, nil)
		r.NoError(err)

		_, err = env.svc.UpdateIntegration(env.ctx, env.org.Slug, created.UID, integrations.UpdateIntegrationRequest{
			Settings: map[string]any{"url": "https://example.com/hook", secretPreviousKey: "whsec_a=bc"},
		})
		r.ErrorIs(err, integrations.ErrInvalidSettings)
	})

	t.Run("explicit empty clears", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		env := newWebhookTestEnv(t)
		created, err := createWebhook(t, env, nil)
		r.NoError(err)

		_, err = env.svc.UpdateIntegration(env.ctx, env.org.Slug, created.UID, integrations.UpdateIntegrationRequest{
			Settings: map[string]any{"url": "https://example.com/hook", secretKey: ""},
		})
		r.NoError(err)

		_, has := storedSettings(t, env, created.UID)[secretKey]
		r.False(has, "an explicit empty secret clears it (auto-generated on next delivery)")
	})

	t.Run("rename leaves a legacy stored secret alone", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		env := newWebhookTestEnv(t)
		created, err := createWebhook(t, env, nil)
		r.NoError(err)

		// A short legacy secret: outside the input range, but it must keep
		// working since the request does not touch it.
		short := legacySecret(legacyKey[:16])
		plantStoredSecrets(t, env, created.UID, map[string]any{secretKey: short})

		name := "renamed"
		_, err = env.svc.UpdateIntegration(env.ctx, env.org.Slug, created.UID, integrations.UpdateIntegrationRequest{
			Name: &name,
		})
		r.NoError(err)
		r.Equal(short, storedSettings(t, env, created.UID)[secretKey])
	})

	t.Run("dashboard round-trip of the displayed secret succeeds", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		env := newWebhookTestEnv(t)
		created, err := createWebhook(t, env, nil)
		r.NoError(err)

		short := legacySecret(legacyKey[:16])
		plantStoredSecrets(t, env, created.UID, map[string]any{
			secretKey:         short,
			secretPreviousKey: legacySecret(legacyKey),
		})

		// The dashboard sends back the whole settings map it was shown, in
		// the canonical spelling GET rendered.
		got, err := env.svc.GetIntegration(env.ctx, env.org.Slug, created.UID)
		r.NoError(err)

		_, err = env.svc.UpdateIntegration(env.ctx, env.org.Slug, created.UID, integrations.UpdateIntegrationRequest{
			Settings: got.Settings,
		})
		r.NoError(err, "echoing the displayed secrets back must not be rejected")

		stored := storedSettings(t, env, created.UID)
		r.Equal(standardSecret(legacyKey[:16]), stored[secretKey], "same key, canonical spelling")
		r.Equal(standardSecret(legacyKey), stored[secretPreviousKey])
	})
}

func TestWebhookChannel_LegacySecretsReturnedCanonical(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newWebhookTestEnv(t)

	created, err := createWebhook(t, env, nil)
	r.NoError(err)

	prevKey := append([]byte{0xff, 0xfe}, legacyKey[2:]...)
	plantStoredSecrets(t, env, created.UID, map[string]any{
		secretKey:         legacySecret(legacyKey),
		secretPreviousKey: legacySecret(prevKey),
	})

	got, err := env.svc.GetIntegration(env.ctx, env.org.Slug, created.UID)
	r.NoError(err)
	r.Equal(standardSecret(legacyKey), got.Settings[secretKey])
	r.Equal(standardSecret(prevKey), got.Settings[secretPreviousKey])

	stored := storedSettings(t, env, created.UID)
	r.Equal(legacySecret(legacyKey), stored[secretKey], "a read must not rewrite storage")
	r.Equal(legacySecret(prevKey), stored[secretPreviousKey], "a read must not rewrite storage")

	rotated, err := env.svc.RotateWebhookSecret(env.ctx, env.org.Slug, created.UID)
	r.NoError(err)

	newSecret, ok := rotated.Settings[secretKey].(string)
	r.True(ok)
	requireStandardSecret(r, newSecret)
	r.Equal(standardSecret(legacyKey), rotated.Settings[secretPreviousKey],
		"the rotated-out legacy secret is shown in standard form")
	r.Equal(legacySecret(legacyKey), storedSettings(t, env, created.UID)[secretPreviousKey],
		"rotation moves the stored value as is")
}

func TestWebhookChannel_UndecodableStoredSecretReturnedUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newWebhookTestEnv(t)

	created, err := createWebhook(t, env, nil)
	r.NoError(err)

	plantStoredSecrets(t, env, created.UID, map[string]any{secretKey: "whsec_not base64!!"})

	got, err := env.svc.GetIntegration(env.ctx, env.org.Slug, created.UID)
	r.NoError(err)
	r.Equal("whsec_not base64!!", got.Settings[secretKey])
}
