package credmigrate_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/credmigrate"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func TestRunRequiresEnabledCredentials(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	_, err = credmigrate.Run(ctx, dbSvc, nil, credmigrate.Options{})
	require.ErrorIs(t, err, credmigrate.ErrDisabled)

	disabled, err := credentials.NewService(nil, newMemDEKStore())
	require.NoError(t, err)

	_, err = credmigrate.Run(ctx, dbSvc, disabled, credmigrate.Options{})
	require.ErrorIs(t, err, credmigrate.ErrDisabled)
}

func TestRunEncryptsPlaintextSecrets(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	org := models.NewOrganization("run-enc", "Run Enc Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	leaky := seedLeakyCheck(ctx, t, dbSvc, org, "leaky-ssh",
		map[string]any{"host": "lan.internal", "private_key": "SSH-SECRET"})
	clean := seedLeakyCheck(ctx, t, dbSvc, org, "clean-tcp", map[string]any{"host": "acme.com"})

	conn := models.NewIntegration(org.UID, models.ConnectionTypeWebhook, "leaky-hook")
	conn.Settings = models.JSONMap{"signingSecret": "whsec_abc", "auth_token": "tok"}
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	opts := credmigrate.Options{Logger: slog.Default()}

	// Dry run reports but writes nothing.
	opts.DryRun = true
	stats, err := credmigrate.Run(ctx, dbSvc, creds, opts)
	r.NoError(err)
	r.Equal(1, stats.ChecksMigrated)
	r.Equal(1, stats.ConnectionsMigrated)

	got, err := dbSvc.GetCheck(ctx, org.UID, leaky.UID)
	r.NoError(err)
	r.Nil(got.ConfigPrivate)

	// Real run encrypts.
	opts.DryRun = false
	stats, err = credmigrate.Run(ctx, dbSvc, creds, opts)
	r.NoError(err)
	r.Equal(1, stats.ChecksMigrated)
	r.GreaterOrEqual(stats.ChecksScanned, 2)
	r.Equal(1, stats.ConnectionsMigrated)

	got, err = dbSvc.GetCheck(ctx, org.UID, leaky.UID)
	r.NoError(err)
	r.NotNil(got.ConfigPrivate)
	r.NotContains(got.Config, "private_key")

	gotClean, err := dbSvc.GetCheck(ctx, org.UID, clean.UID)
	r.NoError(err)
	r.Nil(gotClean.ConfigPrivate)

	gotConn, err := dbSvc.GetChannel(ctx, conn.UID)
	r.NoError(err)
	r.NotNil(gotConn.SettingsPrivate)

	// Idempotent.
	stats, err = credmigrate.Run(ctx, dbSvc, creds, opts)
	r.NoError(err)
	r.Zero(stats.ChecksMigrated)
	r.Zero(stats.ConnectionsMigrated)
}
