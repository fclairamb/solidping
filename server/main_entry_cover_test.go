package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/internal/config"
)

// isolateEnv drops every SP_* variable of the developer's environment, moves
// into an empty directory (no config.yml) and points the server at a fresh
// SQLite database. The tests using it cannot run in parallel: they edit the
// process environment.
func isolateEnv(t *testing.T) {
	t.Helper()

	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "SP_") || strings.HasPrefix(name, "SOLIDPING_") {
			t.Setenv(name, "")
			require.NoError(t, os.Unsetenv(name))
		}
	}

	t.Chdir(t.TempDir())

	dir := t.TempDir()
	t.Setenv("SP_DB_TYPE", "sqlite")
	t.Setenv("SP_DB_DIR", dir)
	t.Setenv("SP_LOG_LEVEL", "error")
	t.Setenv("SP_LOG_FORMAT", "text")
}

func freeListen(t *testing.T) string {
	t.Helper()

	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := lis.Addr().String()
	require.NoError(t, lis.Close())

	return addr
}

func TestMigrateAndRepairCommands(t *testing.T) { //nolint:paralleltest // edits the process environment
	isolateEnv(t)

	ctx := context.Background()

	require.NoError(t, migrate(ctx, nil))
	// Second run: nothing pending, repair finds nothing to re-record.
	require.NoError(t, migrate(ctx, nil))
	require.NoError(t, migrateRepair(ctx, nil))
}

func TestMigrateRejectsUnsupportedDatabase(t *testing.T) {
	isolateEnv(t)
	t.Setenv("SP_DB_TYPE", "oracle")

	ctx := context.Background()

	require.Error(t, migrate(ctx, nil))
	require.Error(t, migrateRepair(ctx, nil))
	require.Error(t, encryptCredentials(ctx, &cli.Command{}))

	_, err := openDB(ctx, &config.Config{Database: config.DatabaseConfig{Type: "oracle"}})
	require.Error(t, err)

	require.Error(t, runMigrations(ctx, &config.Config{Database: config.DatabaseConfig{Type: "oracle"}}))
}

func TestRunMigrationsSQLiteMemory(t *testing.T) { //nolint:paralleltest // shares the default logger
	cfg := &config.Config{Database: config.DatabaseConfig{Type: "sqlite-memory"}}

	require.NoError(t, runMigrations(context.Background(), cfg))

	dbSvc, err := openDB(context.Background(), cfg)
	require.NoError(t, err)
	require.NoError(t, dbSvc.Close())
}

func TestEncryptCredentialsCommand(t *testing.T) {
	isolateEnv(t)

	ctx := context.Background()

	// No master key: refuses with a hint.
	err := encryptCredentials(ctx, &cli.Command{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "encryption disabled")

	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)

	t.Setenv("SP_ENCRYPTION_MASTER_KEY", base64.StdEncoding.EncodeToString(key))

	require.NoError(t, encryptCredentials(ctx, &cli.Command{}))
}

func TestHealthcheckAction(t *testing.T) {
	isolateEnv(t)

	addr := freeListen(t)
	t.Setenv("SP_SERVER_LISTEN", addr)

	ctx := context.Background()

	// Nothing listens: the check fails with exit code 1.
	err := healthcheckAction(ctx, nil)
	require.Error(t, err)

	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr)
	require.NoError(t, err)

	srv := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	}

	go func() { _ = srv.Serve(lis) }()

	t.Cleanup(func() { _ = srv.Close() })

	require.NoError(t, healthcheckAction(ctx, nil))
}

func TestLoadServeConfigInvalid(t *testing.T) {
	isolateEnv(t)
	t.Setenv("SP_DB_TYPE", "oracle")

	_, _, err := loadServeConfig(context.Background(), os.Stderr)
	require.Error(t, err)
}

func TestWatchParent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	got, stop := watchParent(ctx, &config.Config{})
	require.Equal(t, ctx, got)
	stop()

	cfg := &config.Config{}
	cfg.Server.ExitWithParent = true

	got, stop = watchParent(ctx, cfg)
	require.NotEqual(t, ctx, got)
	stop()
	require.Error(t, got.Err())
}

func TestShortSum(t *testing.T) {
	t.Parallel()

	require.Equal(t, "abc", shortSum("abc"))
	require.Equal(t, "0123456789ab", shortSum("0123456789abcdef"))
	require.Empty(t, shortSum(""))
}

func TestServeStartsAndStopsInTestMode(t *testing.T) {
	isolateEnv(t)

	addr := freeListen(t)
	t.Setenv("SP_SERVER_LISTEN", addr)
	t.Setenv("SP_RUNMODE", "test")
	t.Setenv("SP_SHUTDOWN_TIMEOUT", "5s")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- serve(ctx, nil) }()

	require.Eventually(t, func() bool {
		resp, err := http.Get(fmt.Sprintf("http://%s/api/mgmt/health", addr)) //nolint:noctx // test probe
		if err != nil {
			return false
		}

		defer func() { _ = resp.Body.Close() }()

		return resp.StatusCode == http.StatusOK
	}, 60*time.Second, 100*time.Millisecond)

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not stop after the context was canceled")
	}
}

func TestServeFailsOnBrokenDatabase(t *testing.T) {
	isolateEnv(t)

	// A file where the data directory should be.
	file := t.TempDir() + "/notadir"
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	t.Setenv("SP_DB_DIR", file+"/sub")

	require.Error(t, serve(context.Background(), nil))
}
