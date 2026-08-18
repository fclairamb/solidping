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

// setupEncryptedIntegrationsService builds an integrations service WITH a
// master key configured — the real AES-256-GCM envelope path (as opposed to
// setupPlaintextIntegrationsService's self-hosted plaintext-envelope
// fallback), so the accessToken round-trip below proves genuine encryption at
// rest rather than just "the value isn't in the public settings column".
func setupEncryptedIntegrationsService(
	t *testing.T,
) (*integrations.Service, db.Service, credentials.Service, *models.Organization) {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	require.NoError(t, err)
	require.True(t, creds.Enabled(), "this suite exercises the real encryption path")

	org := models.NewOrganization("matrix-secret-test", "Matrix Secret Test Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	svc := integrations.NewService(dbSvc, creds, nil, nil)

	return svc, dbSvc, creds, org
}

// TestMatrixAccessTokenEncryptedAtRestAndRecoverable is the security
// requirement pinned by the spec: `accessToken` must be listed in
// connectionSecretFields so it lands in the encrypted settings_private
// envelope and never sits in plaintext JSONB. A test that only asserts "the
// token is absent from the public column" would also pass if the token were
// silently dropped — so this pairs that negative assertion with a positive
// control: the token must still be recoverable, byte-for-byte, by decrypting
// the envelope with the org's real key.
func TestMatrixAccessTokenEncryptedAtRestAndRecoverable(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, creds, org := setupEncryptedIntegrationsService(t)
	ctx := t.Context()

	const token = "syt_super_secret_access_token"

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type: "matrix",
		Name: "Ops Matrix Room",
		Settings: map[string]any{
			"homeserverUrl": "https://matrix.org",
			"accessToken":   token,
			"roomId":        "!abcdef:matrix.org",
		},
	})
	r.NoError(err)

	// Negative: the API response never echoes the token, but advertises it.
	r.NotContains(created.Settings, "accessToken", "the access token must never be in an API response")
	r.Contains(created.SettingsPrivateKeys, "accessToken")
	// Public (non-secret) fields stay visible so the edit form can render them.
	r.Equal("https://matrix.org", created.Settings["homeserverUrl"])
	r.Equal("!abcdef:matrix.org", created.Settings["roomId"])

	// GET must not leak it either.
	got, err := svc.GetIntegration(ctx, org.Slug, created.UID)
	r.NoError(err)
	r.NotContains(got.Settings, "accessToken", "GET must not leak the token")
	r.Contains(got.SettingsPrivateKeys, "accessToken")

	// The stored PUBLIC column must not carry the token — it lives encrypted
	// in settings_private, and (unlike the no-master-key fallback) that
	// envelope is a real AES-256-GCM ciphertext, not a plaintext wrapper.
	row, err := dbSvc.GetChannel(ctx, created.UID)
	r.NoError(err)
	r.NotContains(row.Settings, "accessToken", "the public settings column must never carry the token")
	r.NotNil(row.SettingsPrivate)
	r.False(credentials.IsPlaintextEnvelope(*row.SettingsPrivate),
		"a configured master key must produce a real encrypted envelope, not the plaintext fallback")
	r.NotContains(*row.SettingsPrivate, token, "the raw token must never appear in the stored ciphertext")

	// Positive control: the token round-trips back to its real value when
	// decrypted with the org's key.
	private, err := creds.DecryptForOrg(ctx, org.UID, *row.SettingsPrivate)
	r.NoError(err)
	r.Equal(token, private["accessToken"], "the connection must still hold its real access token")

	// PATCH omitting the token (dashboard edit — it never received it)
	// preserves the encrypted value rather than wiping it.
	_, err = svc.UpdateIntegration(ctx, org.Slug, created.UID, integrations.UpdateIntegrationRequest{
		Settings: map[string]any{
			"homeserverUrl": "https://matrix.org",
			"roomId":        "!abcdef:matrix.org",
		},
	})
	r.NoError(err)

	after, err := dbSvc.GetChannel(ctx, created.UID)
	r.NoError(err)
	r.NotNil(after.SettingsPrivate, "PATCH without the token must not wipe the stored credential")
	afterPrivate, err := creds.DecryptForOrg(ctx, org.UID, *after.SettingsPrivate)
	r.NoError(err)
	r.Equal(token, afterPrivate["accessToken"], "the round-trip must not lose the token")
}

// TestMatrixConnectionResponseRedactsUnsplitRow is the defense-in-depth test:
// a row whose accessToken was somehow left in the public settings column is
// redacted at read time and the key advertised via settingsPrivateKeys, so no
// secret value can ever leave the API even if the split step is bypassed.
func TestMatrixConnectionResponseRedactsUnsplitRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, _, org := setupEncryptedIntegrationsService(t)
	ctx := t.Context()

	created, err := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
		Type: "matrix",
		Name: "Leaky Matrix Room",
		Settings: map[string]any{
			"homeserverUrl": "https://matrix.org",
			"roomId":        "!abcdef:matrix.org",
		},
	})
	r.NoError(err)

	// Plant the token directly in the public settings column, no envelope.
	leaky := models.JSONMap{
		"homeserverUrl": "https://matrix.org",
		"roomId":        "!abcdef:matrix.org",
		"accessToken":   "LEAKED-TOKEN",
	}
	r.NoError(dbSvc.UpdateChannel(ctx, created.UID, &models.IntegrationUpdate{
		Settings:             &leaky,
		ClearSettingsPrivate: true,
	}))

	got, err := svc.GetIntegration(ctx, org.Slug, created.UID)
	r.NoError(err)
	r.NotContains(got.Settings, "accessToken", "read-time redaction must strip an un-split secret")
	r.Contains(got.SettingsPrivateKeys, "accessToken")
	r.Equal("https://matrix.org", got.Settings["homeserverUrl"])
}
