package checkpostgres_test

import (
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkpostgres"
)

// acceptingSocket listens on a Unix socket and counts accepted connections;
// it closes them at once (a real server on a shared socket is out of unit
// scope: the dial path is what is under test here).
func acceptingSocket(t *testing.T) (string, *atomic.Int32) {
	t.Helper()

	dir, err := os.MkdirTemp("", "pgs")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, ".s.PGSQL.5432")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	require.NoError(t, err)

	t.Cleanup(func() { _ = listener.Close() })

	var accepted atomic.Int32

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			accepted.Add(1)
			_ = conn.Close()
		}
	}()

	return path, &accepted
}

func runPostgresSocket(t *testing.T, path string, allow []string) *checkerdef.Result {
	t.Helper()

	cfg := &checkpostgres.PostgreSQLConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{"socket": path, "username": "monitor", "timeout": "3s"}))

	ctx := checkerdef.WithLocalSocketAllowlist(t.Context(), allow)

	result, err := (&checkpostgres.PostgreSQLChecker{}).Execute(ctx, cfg)
	require.NoError(t, err)

	return result
}

func TestPostgresSocketGoesThroughSharedDialer(t *testing.T) {
	t.Parallel()

	path, accepted := acceptingSocket(t)

	// Positive control: the allowlisted socket is dialed (the fake server then
	// hangs up, so the check is not up, but the connection was made).
	result := runPostgresSocket(t, path, []string{filepath.Dir(path) + "/"})
	require.NotEqual(t, checkerdef.StatusUp, result.Status)
	require.Positive(t, accepted.Load(), "the allowlisted socket must be dialed")

	// Outside the allowlist the dial is refused before any connection.
	accepted.Store(0)

	result = runPostgresSocket(t, path, []string{"/run/other/"})
	require.NotEqual(t, checkerdef.StatusUp, result.Status)
	require.Contains(t, result.Output[checkerdef.OutputKeyError], checkerdef.LocalSocketSetting)
	require.Zero(t, accepted.Load(), "a refused path must not be dialed")

	// A missing socket is down with ENOENT.
	result = runPostgresSocket(t, filepath.Join(filepath.Dir(path), "missing"), []string{filepath.Dir(path) + "/"})
	require.Contains(t, result.Output[checkerdef.OutputKeyError], "ENOENT")
}

func TestPostgresSocketConfig(t *testing.T) {
	t.Parallel()

	validate := func(cfg map[string]any) error {
		return (&checkpostgres.PostgreSQLChecker{}).Validate(&checkerdef.CheckSpec{Config: cfg})
	}

	require.NoError(t, validate(map[string]any{"socket": "/var/run/postgresql/.s.PGSQL.5432", "username": "monitor"}))
	require.ErrorContains(t, validate(map[string]any{
		"socket": "/var/run/postgresql/.s.PGSQL.5432", "username": "monitor", "host": "acme.com",
	}), "mutually exclusive")
	require.Error(t, validate(map[string]any{
		"socket": "/var/run/postgresql/.s.PGSQL.5432", "username": "monitor", "ssl_mode": "require",
	}), "no TLS over a socket")

	cfg := &checkpostgres.PostgreSQLConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{
		"url": "postgres+unix:///var/run/postgresql/.s.PGSQL.5432?dbname=app&user=monitor",
	}))
	require.Equal(t, "/var/run/postgresql/.s.PGSQL.5432", cfg.Socket)
	require.Equal(t, "app", cfg.Database)
	require.Equal(t, "monitor", cfg.Username)
}
