package checkunixsocket

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// socketPath returns a socket path in a fresh directory. The directory is
// created under /tmp rather than t.TempDir(): a Unix socket path is capped at
// 104 bytes on macOS, and t.TempDir() embeds the test name.
func socketPath(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("/tmp", "spus")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return filepath.Join(dir, "s.sock")
}

// listen opens a Unix listener on a fresh path and serves each connection
// with handle until the test ends.
func listen(t *testing.T, handle func(net.Conn)) string {
	t.Helper()

	path := socketPath(t)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}

			go handle(conn)
		}
	}()

	return path
}

func run(t *testing.T, ctx context.Context, cfgMap map[string]any) *checkerdef.Result { //nolint:revive // test helper
	t.Helper()

	cfg := &UnixSocketConfig{}
	require.NoError(t, cfg.FromMap(cfgMap))
	require.NoError(t, cfg.Validate())

	result, err := (&UnixSocketChecker{}).Execute(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, result)

	return result
}

const (
	timeoutWait = 2 * time.Second
	neverWait   = 200 * time.Millisecond
	tick        = 10 * time.Millisecond
)
