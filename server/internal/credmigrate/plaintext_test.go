package credmigrate_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/credmigrate"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// seedLeakyCheck inserts a check whose secret sits in the PUBLIC config column
// with a NULL private column — exactly what the pre-fix plaintext fallback
// wrote, and what the migration must clean up.
func seedLeakyCheck(
	ctx context.Context, t *testing.T, dbSvc *sqlite.Service,
	org *models.Organization, slug string, config map[string]any,
) *models.Check {
	t.Helper()

	check := models.NewCheck(org.UID, slug, "ssh")
	check.Config = models.JSONMap(config)
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	return check
}

// TestRunPlaintextSplitsPublicSecretsAcrossAllTables is the item-2 acceptance
// test: pre-existing rows with a secret in the public column (checks,
// check_jobs, connections) are migrated into a plaintext envelope on startup and
// no longer leak, while already-clean rows are left untouched. Idempotent.
func TestRunPlaintextSplitsPublicSecretsAcrossAllTables(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	disabled, err := credentials.NewService(nil, newMemDEKStore())
	r.NoError(err)

	org := models.NewOrganization("plaintext-mig", "Plaintext Migration Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	// Leaky check: private_key in the public column, NULL private column.
	// CreateCheck also denormalizes a dispatch (check_jobs) row carrying the
	// same leak, so this one seed covers both the checks and check_jobs surfaces.
	leakyCheck := seedLeakyCheck(ctx, t, dbSvc, org, "leaky-ssh",
		map[string]any{"host": "lan.internal", "private_key": "SSH-SECRET"})
	r.Nil(leakyCheck.ConfigPrivate, "seed precondition: nothing in the private column")

	seededJobs, err := dbSvc.ListCheckJobsByCheckUID(ctx, leakyCheck.UID)
	r.NoError(err)
	r.NotEmpty(seededJobs, "CreateCheck must denormalize at least one dispatch row")
	r.Contains(seededJobs[0].Config, "private_key", "seed precondition: the job leaks too")
	job := seededJobs[0]

	// A clean check with no secrets — must be left untouched (control).
	cleanCheck := seedLeakyCheck(ctx, t, dbSvc, org, "clean-tcp",
		map[string]any{"host": "example.com"})

	// A leaky webhook connection: auth_token in the public settings column.
	conn := models.NewIntegration(org.UID, models.ConnectionTypeWebhook, "leaky-hook")
	conn.Settings = models.JSONMap{"webhook_url": "https://x.test/hook", "auth_token": "TOK-SECRET"}
	r.NoError(dbSvc.CreateChannel(ctx, conn))

	// ── Run the plaintext migration ──
	stats, err := credmigrate.RunPlaintext(ctx, dbSvc, credmigrate.Options{})
	r.NoError(err)
	r.Equal(1, stats.ChecksMigrated, "only the leaky check should migrate")
	r.GreaterOrEqual(stats.CheckJobsMigrated, 1, "the leaky check's dispatch row(s) must migrate")
	r.Equal(1, stats.ConnectionsMigrated)

	// ── Check row: secret gone from public column, in a plaintext envelope ──
	gotCheck, err := dbSvc.GetCheck(ctx, org.UID, leakyCheck.UID)
	r.NoError(err)
	r.NotContains(gotCheck.Config, "private_key", "the public config must no longer carry the secret")
	r.Equal("lan.internal", gotCheck.Config["host"])
	r.NotNil(gotCheck.ConfigPrivate)
	r.True(credentials.IsPlaintextEnvelope(*gotCheck.ConfigPrivate))
	checkSecret, err := disabled.DecryptForOrg(ctx, org.UID, *gotCheck.ConfigPrivate)
	r.NoError(err)
	r.Equal("SSH-SECRET", checkSecret["private_key"])
	r.Equal([]string{"private_key"}, privateKeysOf(t, gotCheck.ConfigPrivateKeys))

	// ── Check-job row ──
	gotJob, err := dbSvc.GetCheckJobByUID(ctx, job.UID)
	r.NoError(err)
	r.NotContains(gotJob.Config, "private_key", "the job's public config must no longer carry the secret")
	r.NotNil(gotJob.ConfigPrivate)
	r.True(credentials.IsPlaintextEnvelope(*gotJob.ConfigPrivate))
	r.True(gotJob.Encrypted, "a job with a split-out envelope is flagged encrypted for dispatch gating")
	jobSecret, err := disabled.DecryptForOrg(ctx, org.UID, *gotJob.ConfigPrivate)
	r.NoError(err)
	r.Equal("SSH-SECRET", jobSecret["private_key"])

	// ── Connection row ──
	gotConn, err := dbSvc.GetChannel(ctx, conn.UID)
	r.NoError(err)
	r.NotContains(gotConn.Settings, "auth_token", "the public settings must no longer carry the token")
	r.Equal("https://x.test/hook", gotConn.Settings["webhook_url"])
	r.NotNil(gotConn.SettingsPrivate)
	r.True(credentials.IsPlaintextEnvelope(*gotConn.SettingsPrivate))
	connSecret, err := disabled.DecryptForOrg(ctx, org.UID, *gotConn.SettingsPrivate)
	r.NoError(err)
	r.Equal("TOK-SECRET", connSecret["auth_token"])

	// ── Control row: the clean check is untouched ──
	gotClean, err := dbSvc.GetCheck(ctx, org.UID, cleanCheck.UID)
	r.NoError(err)
	r.Nil(gotClean.ConfigPrivate, "a check with no secrets must not gain an envelope")

	// ── Idempotency: a second pass migrates nothing ──
	stats2, err := credmigrate.RunPlaintext(ctx, dbSvc, credmigrate.Options{})
	r.NoError(err)
	r.Zero(stats2.Migrated(), "already-split rows must not be re-migrated")
}

// privateKeysOf decodes a *string JSON key list for assertions.
func privateKeysOf(t *testing.T, raw *string) []string {
	t.Helper()

	r := require.New(t)
	r.NotNil(raw)

	var keys []string
	r.NoError(json.Unmarshal([]byte(*raw), &keys))

	return keys
}
