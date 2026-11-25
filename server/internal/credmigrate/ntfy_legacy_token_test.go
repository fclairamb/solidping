package credmigrate_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/credmigrate"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func newNtfyReconcileDB(t *testing.T, slug string) (*sqlite.Service, *models.Organization) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization(slug, "Ntfy Legacy Token Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return dbSvc, org
}

// TestReconcileConnectionRegistry_MovesNtfyLegacyAccessToken covers an ntfy
// row that already had an encrypted auth_token but kept its legacy
// accessToken in the public settings (written before accessToken was a
// declared secret). Run skips it (its private column is set), so the
// reconcile must move accessToken into the envelope, keep auth_token, and
// leave server_url and topic public.
func TestReconcileConnectionRegistry_MovesNtfyLegacyAccessToken(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	dbSvc, org := newNtfyReconcileDB(t, "reconcile-ntfy")

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	conn := seedEncryptedConnection(ctx, t, dbSvc, creds, org, models.ConnectionTypeNtfy,
		map[string]any{"server_url": "https://ntfy.sh", "topic": "alerts", "accessToken": "tk_legacy"},
		map[string]any{"auth_token": "tk_canonical"})

	stats, err := credmigrate.ReconcileConnectionRegistry(ctx, dbSvc, creds, credmigrate.Options{})
	r.NoError(err)
	r.Equal(1, stats.ConnectionsReconciled)

	got, err := dbSvc.GetChannel(ctx, conn.UID)
	r.NoError(err)
	r.NotContains(got.Settings, "accessToken", "the public settings must no longer hold the legacy token")
	r.Equal("https://ntfy.sh", got.Settings["server_url"])
	r.Equal("alerts", got.Settings["topic"])
	r.ElementsMatch([]string{"accessToken", "auth_token"}, privateKeysOf(t, got.SettingsPrivateKeys))
	r.NotNil(got.SettingsPrivate)
	r.NotContains(*got.SettingsPrivate, "tk_legacy")

	dec, err := creds.DecryptForOrg(ctx, org.UID, *got.SettingsPrivate)
	r.NoError(err)
	r.Equal("tk_legacy", dec["accessToken"])
	r.Equal("tk_canonical", dec["auth_token"])

	// Idempotent: a second pass touches nothing.
	stats2, err := credmigrate.ReconcileConnectionRegistry(ctx, dbSvc, creds, credmigrate.Options{})
	r.NoError(err)
	r.Zero(stats2.ConnectionsReconciled)
}

// TestReconcileConnectionRegistry_SplitsNtfyLegacyOnlyRow covers the common
// legacy row: the token only under accessToken, in the public settings, no
// private envelope.
func TestReconcileConnectionRegistry_SplitsNtfyLegacyOnlyRow(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	dbSvc, org := newNtfyReconcileDB(t, "reconcile-ntfy-only")

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	conn := models.NewIntegration(org.UID, models.ConnectionTypeNtfy, "legacy")
	conn.Settings = models.JSONMap{"server_url": "https://ntfy.sh", "topic": "alerts", "accessToken": "tk_legacy"}
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	stats, err := credmigrate.ReconcileConnectionRegistry(ctx, dbSvc, creds, credmigrate.Options{})
	r.NoError(err)
	r.Equal(1, stats.ConnectionsReconciled)

	got, err := dbSvc.GetChannel(ctx, conn.UID)
	r.NoError(err)
	r.NotContains(got.Settings, "accessToken")
	r.Equal("alerts", got.Settings["topic"])
	r.Equal([]string{"accessToken"}, privateKeysOf(t, got.SettingsPrivateKeys))
	r.NotNil(got.SettingsPrivate)
	r.False(credentials.IsPlaintextEnvelope(*got.SettingsPrivate))

	dec, err := creds.DecryptForOrg(ctx, org.UID, *got.SettingsPrivate)
	r.NoError(err)
	r.Equal("tk_legacy", dec["accessToken"])
}

// TestRunPlaintextMovesNtfyLegacyAccessToken is the no-master-key side: a row
// whose plaintext envelope already holds auth_token but whose public settings
// still carry accessToken gets both keys in a re-sealed plaintext envelope.
func TestRunPlaintextMovesNtfyLegacyAccessToken(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)
	dbSvc, org := newNtfyReconcileDB(t, "plaintext-ntfy")

	envelope, err := credentials.SealPlaintext(map[string]any{"auth_token": "tk_canonical"})
	r.NoError(err)

	keys := `["auth_token"]`
	conn := models.NewIntegration(org.UID, models.ConnectionTypeNtfy, "legacy")
	conn.Settings = models.JSONMap{"server_url": "https://ntfy.sh", "topic": "alerts", "accessToken": "tk_legacy"}
	conn.SettingsPrivate = &envelope
	conn.SettingsPrivateKeys = &keys
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	stats, err := credmigrate.RunPlaintext(ctx, dbSvc, credmigrate.Options{})
	r.NoError(err)
	r.Equal(1, stats.ConnectionsMigrated)

	got, err := dbSvc.GetChannel(ctx, conn.UID)
	r.NoError(err)
	r.NotContains(got.Settings, "accessToken")
	r.Equal("alerts", got.Settings["topic"])
	r.ElementsMatch([]string{"accessToken", "auth_token"}, privateKeysOf(t, got.SettingsPrivateKeys))

	private, err := credentials.OpenPlaintext(*got.SettingsPrivate)
	r.NoError(err)
	r.Equal("tk_legacy", private["accessToken"])
	r.Equal("tk_canonical", private["auth_token"])

	stats2, err := credmigrate.RunPlaintext(ctx, dbSvc, credmigrate.Options{})
	r.NoError(err)
	r.Zero(stats2.Migrated())
}
