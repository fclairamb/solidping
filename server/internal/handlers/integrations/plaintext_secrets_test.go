package integrations_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
)

// setupPlaintextIntegrationsService builds an integrations service with NO
// master key — the self-hosted plaintext fallback. Connection secrets must
// still be split out of the public `settings` column into a plaintext envelope.
func setupPlaintextIntegrationsService(
	t *testing.T,
) (*integrations.Service, db.Service, credentials.Service, *models.Organization) {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(nil, newMemDEKStore())
	require.NoError(t, err)
	require.False(t, creds.Enabled(), "this suite is the no-master-key path")

	org := models.NewOrganization("plaintext-conn-test", "Plaintext Connection Test Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	svc := integrations.NewService(dbSvc, creds, nil, nil)

	return svc, dbSvc, creds, org
}

// TestPlaintextModeConnectionSecretNeverLeaks is the connection mirror of the
// checks acceptance test: with no master key, a connection's auth token never
// appears in the API response nor in the public settings column, is advertised
// via settingsPrivateKeys, and round-trips (positive control) so the connection
// still works. A PATCH that omits it preserves it.
func TestPlaintextModeConnectionSecretNeverLeaks(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, creds, org := setupPlaintextIntegrationsService(t)
	ctx := t.Context()

	const token = "tok-super-secret"

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type: "webhook",
		Name: "Ops Webhook",
		Settings: map[string]any{
			"webhook_url": "https://example.com/hook",
			"auth_token":  token,
		},
	})
	r.NoError(err)

	// The API response must advertise, never echo, the token.
	r.NotContains(created.Settings, "auth_token", "the token must never be in an API response")
	r.Contains(created.SettingsPrivateKeys, "auth_token")
	r.Equal("https://example.com/hook", created.Settings["webhook_url"], "public URL stays visible")

	// GET must not leak it either.
	got, err := svc.GetIntegration(ctx, org.Slug, created.UID)
	r.NoError(err)
	r.NotContains(got.Settings, "auth_token", "GET must not leak the token")
	r.Contains(got.SettingsPrivateKeys, "auth_token")

	// The stored PUBLIC column must not carry the token — it lives in a
	// plaintext envelope in settings_private.
	row, err := dbSvc.GetChannel(ctx, created.UID)
	r.NoError(err)
	r.NotContains(row.Settings, "auth_token", "the public settings column must never carry the token")
	r.NotNil(row.SettingsPrivate)
	r.True(credentials.IsPlaintextEnvelope(*row.SettingsPrivate),
		"no master key ⇒ the private side is a plaintext envelope, not a merge back into public settings")

	// Positive control: the token round-trips back to its real value.
	private, err := creds.DecryptForOrg(ctx, org.UID, *row.SettingsPrivate)
	r.NoError(err)
	r.Equal(token, private["auth_token"], "the connection must still hold its real token")

	// PATCH omitting the token (dashboard edit — it never received it) preserves it.
	_, err = svc.UpdateIntegration(ctx, org.Slug, created.UID, integrations.UpdateIntegrationRequest{
		Settings: map[string]any{"webhook_url": "https://example.com/hook2"},
	})
	r.NoError(err)

	after, err := dbSvc.GetChannel(ctx, created.UID)
	r.NoError(err)
	r.NotNil(after.SettingsPrivate, "PATCH without the token wiped the plaintext-mode credential")
	afterPrivate, err := creds.DecryptForOrg(ctx, org.UID, *after.SettingsPrivate)
	r.NoError(err)
	r.Equal(token, afterPrivate["auth_token"], "the round-trip must not lose the token")
	r.Equal("https://example.com/hook2", after.Settings["webhook_url"])
}

// TestConnectionResponseRedactsUnsplitRow is the connection defense-in-depth
// test: a row whose secret was left in the public settings column is redacted
// at read time and the key advertised, so no secret value can leave the API.
func TestConnectionResponseRedactsUnsplitRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, _, org := setupPlaintextIntegrationsService(t)
	ctx := t.Context()

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type:     "webhook",
		Name:     "Leaky Webhook",
		Settings: map[string]any{"webhook_url": "https://example.com/hook"},
	})
	r.NoError(err)

	// Plant the token directly in the public settings column, no envelope.
	leaky := models.JSONMap{"webhook_url": "https://example.com/hook", "auth_token": "LEAKED-TOKEN"}
	r.NoError(dbSvc.UpdateChannel(ctx, created.UID, &models.IntegrationUpdate{
		Settings:             &leaky,
		ClearSettingsPrivate: true,
	}))

	got, err := svc.GetIntegration(ctx, org.Slug, created.UID)
	r.NoError(err)
	r.NotContains(got.Settings, "auth_token", "read-time redaction must strip an un-split secret")
	r.Contains(got.SettingsPrivateKeys, "auth_token")
	r.Equal("https://example.com/hook", got.Settings["webhook_url"])
}
