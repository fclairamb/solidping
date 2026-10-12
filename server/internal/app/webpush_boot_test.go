package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

func webpushBootConfig(dbType, dir string) *config.Config {
	cfg := &config.Config{}
	cfg.Database.Type = dbType
	cfg.Database.Dir = dir
	cfg.Auth.JWTSecret = "webpush-boot-test-secret"

	return cfg
}

func assertCleanWebPushBoot(t *testing.T, logs string) {
	t.Helper()

	require.NotContains(t, logs, "SQL query failed")
	require.NotContains(t, logs, "no such table")
	require.NotContains(t, logs, "webpush:")

	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "level=WARN") || strings.Contains(line, "level=ERROR") {
			require.NotContains(t, line, "app_settings")
		}
	}
}

//nolint:paralleltest // captureLogs swaps the global slog default
func TestWebPushFirstAndSecondBootSQLiteFile(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)
	dir := t.TempDir()

	buf := captureLogs(t)
	cfg1 := webpushBootConfig("sqlite", dir)

	srv, err := NewServer(ctx, cfg1)
	r.NoError(err)
	r.Empty(srv.Services().WebPushOptions.VAPIDPublicKey, "keys must not exist before Initialize")
	r.NoError(srv.Initialize(ctx))

	first := srv.Services().WebPushOptions.VAPIDPublicKey
	r.NotEmpty(first)
	r.Equal(cfg1.WebPush.VAPIDPublicKey, first)
	assertCleanWebPushBoot(t, buf.String())
	r.Equal(1, strings.Count(buf.String(), "Generated VAPID keys"))
	r.NoError(srv.Close(ctx))

	buf.Reset()

	srv2, err := NewServer(ctx, webpushBootConfig("sqlite", dir))
	r.NoError(err)
	r.NoError(srv2.Initialize(ctx))
	t.Cleanup(func() { _ = srv2.Close(ctx) })

	r.Equal(first, srv2.Services().WebPushOptions.VAPIDPublicKey)
	r.NotContains(buf.String(), "Generated VAPID keys")
	assertCleanWebPushBoot(t, buf.String())
}

//nolint:paralleltest // captureLogs swaps the global slog default
func TestWebPushBootSQLiteMemory(t *testing.T) {
	ctx := t.Context()
	r := require.New(t)

	buf := captureLogs(t)

	srv, err := NewServer(ctx, webpushBootConfig("sqlite-memory", ""))
	r.NoError(err)
	r.NoError(srv.Initialize(ctx))
	t.Cleanup(func() { _ = srv.Close(ctx) })

	r.NotEmpty(srv.Services().WebPushOptions.VAPIDPublicKey)
	assertCleanWebPushBoot(t, buf.String())
	r.Equal(1, strings.Count(buf.String(), "Generated VAPID keys"))
}
