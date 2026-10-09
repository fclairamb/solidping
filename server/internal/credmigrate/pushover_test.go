package credmigrate_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/credmigrate"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

func newSQLiteForPushover(t *testing.T) *sqlite.Service {
	t.Helper()
	r := require.New(t)

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(t.Context()))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return dbSvc
}

func TestNormalizePushoverSettings_SQLiteEncrypted(t *testing.T) {
	t.Parallel()

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	require.NoError(t, err)

	assertPushoverBackfill(t.Context(), t, newSQLiteForPushover(t), creds)
}

// TestNormalizePushoverSettings_SQLitePlaintext covers a deployment with no
// master key: the secrets still leave the public settings, into the
// plaintext envelope the keyless fallback reads.
func TestNormalizePushoverSettings_SQLitePlaintext(t *testing.T) {
	t.Parallel()

	assertPushoverBackfill(t.Context(), t, newSQLiteForPushover(t), nil)
}

// assertPushoverBackfill seeds legacy, canonical and unrelated rows, runs the
// backfill twice and checks the first run normalizes only the legacy rows and
// the second run is a no-op. Shared by the SQLite and Postgres tests.
func assertPushoverBackfill(ctx context.Context, t *testing.T, dbSvc db.Service, creds credentials.Service) {
	t.Helper()
	r := require.New(t)

	org := models.NewOrganization("pushover-backfill", "Pushover Backfill")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	seed := func(connType models.ConnectionType, settings models.JSONMap) *models.Integration {
		conn := models.NewIntegration(org.UID, connType, "seed")
		conn.Settings = settings
		r.NoError(dbSvc.CreateChannel(ctx, conn))

		return conn
	}

	dashboardRow := seed(models.ConnectionTypePushover, models.JSONMap{
		"user": "u-dash", "token": "a-dash", "device": "phone",
	})
	senderRow := seed(models.ConnectionTypePushover, models.JSONMap{"userKey": "u-old", "apiToken": "a-old"})
	plainCanonical := seed(models.ConnectionTypePushover, models.JSONMap{"user_key": "u-c", "api_token": "a-c"})
	ntfyRow := seed(models.ConnectionTypeNtfy, models.JSONMap{"topic": "alerts", "token": "not-pushover"})

	// An already-canonical, already-split row must be left alone.
	canonical := models.NewIntegration(org.UID, models.ConnectionTypePushover, "canonical")
	canonical.Settings = models.JSONMap{}
	private := map[string]any{"user_key": "u-ok", "api_token": "a-ok"}

	var envelope string

	var err error
	if creds != nil {
		envelope, err = creds.EncryptForOrg(ctx, org.UID, private)
	} else {
		envelope, err = credentials.SealPlaintext(private)
	}

	r.NoError(err)

	keys := `["api_token","user_key"]`
	canonical.SettingsPrivate = &envelope
	canonical.SettingsPrivateKeys = &keys
	r.NoError(dbSvc.CreateChannel(ctx, canonical))

	stats, err := credmigrate.NormalizePushoverSettings(ctx, dbSvc, creds, credmigrate.Options{})
	r.NoError(err)
	r.Equal(4, stats.Scanned)
	r.Equal(3, stats.Normalized)
	r.Zero(stats.Skipped)

	assertCanonicalPushover(ctx, t, dbSvc, creds, dashboardRow.UID, "u-dash", "a-dash")
	assertCanonicalPushover(ctx, t, dbSvc, creds, senderRow.UID, "u-old", "a-old")
	assertCanonicalPushover(ctx, t, dbSvc, creds, plainCanonical.UID, "u-c", "a-c")
	assertCanonicalPushover(ctx, t, dbSvc, creds, canonical.UID, "u-ok", "a-ok")

	dashboard, err := dbSvc.GetChannel(ctx, dashboardRow.UID)
	r.NoError(err)
	r.Equal("phone", dashboard.Settings["device"], "non-secret settings stay public")

	untouched, err := dbSvc.GetChannel(ctx, canonical.UID)
	r.NoError(err)
	r.Equal(envelope, *untouched.SettingsPrivate, "a canonical row is not rewritten")

	ntfy, err := dbSvc.GetChannel(ctx, ntfyRow.UID)
	r.NoError(err)
	r.Equal("not-pushover", ntfy.Settings["token"], "other integration types are not touched")

	again, err := credmigrate.NormalizePushoverSettings(ctx, dbSvc, creds, credmigrate.Options{})
	r.NoError(err)
	r.Equal(4, again.Scanned)
	r.Zero(again.Normalized, "a second run is a no-op")
}

func assertCanonicalPushover(
	ctx context.Context, t *testing.T, dbSvc db.Service, creds credentials.Service,
	uid, userKey, apiToken string,
) {
	t.Helper()
	r := require.New(t)

	conn, err := dbSvc.GetChannel(ctx, uid)
	r.NoError(err)

	for _, key := range []string{"user", "token", "userKey", "apiToken", "user_key", "api_token"} {
		r.NotContains(conn.Settings, key, "public settings must not carry %s", key)
	}

	r.NotNil(conn.SettingsPrivate)
	if creds != nil {
		r.NotContains(*conn.SettingsPrivate, apiToken, "the token is encrypted at rest")
	}
	r.NotNil(conn.SettingsPrivateKeys)

	var keys []string
	r.NoError(json.Unmarshal([]byte(*conn.SettingsPrivateKeys), &keys))
	r.Equal([]string{"api_token", "user_key"}, keys)

	full, err := credentials.OpenConnectionSettings(ctx, creds, conn)
	r.NoError(err)
	r.Equal(userKey, full["user_key"])
	r.Equal(apiToken, full["api_token"])
}
