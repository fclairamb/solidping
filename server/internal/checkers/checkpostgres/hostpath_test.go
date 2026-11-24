package checkpostgres_test

import (
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkpostgres"
)

// Spec 2026-11-24-08: a filesystem-path host would make lib/pq open a Unix
// socket outside checkers.unix_socket_paths.

func TestPostgresHostPathRefusedByValidate(t *testing.T) {
	t.Parallel()

	validate := func(host string) error {
		return (&checkpostgres.PostgreSQLChecker{}).Validate(&checkerdef.CheckSpec{
			Config: map[string]any{"host": host, "username": "monitor"},
		})
	}

	for _, host := range []string{"/var/run/postgresql", "/tmp", "@pg", "run/postgresql"} {
		err := validate(host)
		require.Error(t, err, host)

		var cfgErr *checkerdef.ConfigError
		require.ErrorAs(t, err, &cfgErr, host)
		require.Equal(t, "host", cfgErr.Parameter, host)
		require.Contains(t, err.Error(), "socket", host)
	}

	for _, host := range []string{"db.acme.com", "10.0.0.5", "::1"} {
		require.NoError(t, validate(host), host)
	}
}

func TestPostgresHostPathNeverDialsSocket(t *testing.T) {
	t.Parallel()

	path, accepted := acceptingSocket(t)

	// Built directly, skipping Validate: a row stored before the rule.
	cfg := &checkpostgres.PostgreSQLConfig{
		Host: filepath.Dir(path), Port: 5432, Username: "monitor", Timeout: 3 * time.Second,
	}
	ctx := checkerdef.WithLocalSocketAllowlist(t.Context(), nil)

	result, err := (&checkpostgres.PostgreSQLChecker{}).Execute(ctx, cfg)
	require.NoError(t, err)
	require.NotEqual(t, checkerdef.StatusUp, result.Status)
	require.Contains(t, result.Output[checkerdef.OutputKeyError], checkerdef.LocalSocketSetting)
	require.Zero(t, accepted.Load(), "a path host must never reach the socket")
}

func TestPostgresTCPHostStillDials(t *testing.T) {
	t.Parallel()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
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

	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)

	cfg := &checkpostgres.PostgreSQLConfig{
		Host: "127.0.0.1", Port: addr.Port, Username: "monitor", SSLMode: "disable", Timeout: 3 * time.Second,
	}

	result, err := (&checkpostgres.PostgreSQLChecker{}).Execute(t.Context(), cfg)
	require.NoError(t, err)
	require.NotEqual(t, checkerdef.StatusUp, result.Status)
	require.Positive(t, accepted.Load(), "a TCP host must still be dialed")
}
