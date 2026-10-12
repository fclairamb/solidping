package app

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

const generatedVAPIDLine = "Generated VAPID keys"

// bootForWebPush runs NewServer then Initialize on a brand-new config, the way
// main does, and returns the server and its captured logs. A new config per
// boot matters: the first boot writes the keys into its config, and reusing it
// would take the env-provisioned path and hide a key that was never stored.
func bootForWebPush(t *testing.T, dbType, dir string) (*Server, string) {
	t.Helper()

	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbType
	cfg.Database.Dir = dir
	cfg.Auth.JWTSecret = "webpush-boot-secret"

	buf := captureLogs(t)

	server, err := NewServer(ctx, cfg)
	r.NoError(err)

	r.Empty(server.Services().WebPushOptions.VAPIDPublicKey,
		"the keys live in app_settings, which does not exist before the migrations")

	r.NoError(server.Initialize(ctx))

	pub := server.Services().WebPushOptions.VAPIDPublicKey
	r.NotEmpty(pub)
	r.Equal(pub, cfg.WebPush.VAPIDPublicKey)

	return server, buf.String()
}

func requireCleanWebPushBoot(t *testing.T, logs string) {
	t.Helper()

	r := require.New(t)
	r.NotContains(logs, "no such table")
	r.NotContains(logs, "SQL query failed")
	r.NotContains(logs, "level=WARN msg=\"webpush:")
}

// TestWebPushKeysCreatedAfterMigrationsAndStable covers spec 2026-10-12-03:
// the first boot of a fresh database creates and stores the keypair without a
// warning, and a restart serves the same public key without generating again.
//
//nolint:paralleltest // captureLogs swaps the process-wide slog default (see its comment)
func TestWebPushKeysCreatedAfterMigrationsAndStable(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()

	first, logs := bootForWebPush(t, "sqlite", dir)
	requireCleanWebPushBoot(t, logs)
	r.Equal(1, strings.Count(logs, generatedVAPIDLine), logs)

	firstKey := first.Services().WebPushOptions.VAPIDPublicKey
	r.NoError(first.dbService.Close())

	second, logs := bootForWebPush(t, "sqlite", dir)
	t.Cleanup(func() { _ = second.dbService.Close() })

	requireCleanWebPushBoot(t, logs)
	r.NotContains(logs, generatedVAPIDLine)
	r.Equal(firstKey, second.Services().WebPushOptions.VAPIDPublicKey,
		"a browser subscribed during the first run must keep working after a restart")
}

// TestWebPushKeysInMemoryBoot is the sqlite-memory variant: one clean boot.
//
//nolint:paralleltest // captureLogs swaps the process-wide slog default (see its comment)
func TestWebPushKeysInMemoryBoot(t *testing.T) {
	server, logs := bootForWebPush(t, dbTypeSQLiteMemory, "")
	t.Cleanup(func() { _ = server.dbService.Close() })

	requireCleanWebPushBoot(t, logs)
	require.Equal(t, 1, strings.Count(logs, generatedVAPIDLine), logs)
}
