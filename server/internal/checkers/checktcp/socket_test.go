package checktcp

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/egress"
)

// socketListener serves every connection on a Unix socket with handle and
// returns its path. The directory is short: macOS caps a socket path at ~104
// bytes.
func socketListener(t *testing.T, handle func(net.Conn)) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "spt")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "app.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			go func() {
				defer func() { _ = conn.Close() }()
				handle(conn)
			}()
		}
	}()

	return path
}

func runSocket(t *testing.T, ctx context.Context, cfg *TCPConfig) *checkerdef.Result { //nolint:revive // test helper
	t.Helper()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	result, err := (&TCPChecker{}).Execute(ctx, cfg)
	require.NoError(t, err)

	return result
}

func TestTCPSocket_ConnectOnly(t *testing.T) {
	t.Parallel()

	path := socketListener(t, func(net.Conn) {})
	ctx := checkerdef.WithLocalSocketAllowlist(context.Background(), []string{path})

	result := runSocket(t, ctx, &TCPConfig{Socket: path, Timeout: 2 * time.Second})
	require.Equal(t, checkerdef.StatusUp, result.Status, "output: %v", result.Output)
	require.Equal(t, path, result.Output["socket"])
}

func TestTCPSocket_SendExpect(t *testing.T) {
	t.Parallel()

	path := socketListener(t, func(conn net.Conn) {
		line, _ := bufio.NewReader(conn).ReadString('\n')
		if line == "PING\r\n" {
			_, _ = conn.Write([]byte("+PONG\r\n"))
		}
	})
	ctx := checkerdef.WithLocalSocketAllowlist(context.Background(), []string{filepath.Dir(path) + "/"})

	result := runSocket(t, ctx, &TCPConfig{
		Socket: path, SendData: "PING\r\n", ExpectData: "+PONG", Timeout: 2 * time.Second,
	})
	require.Equal(t, checkerdef.StatusUp, result.Status, "output: %v", result.Output)
}

func TestTCPSocket_RefusedOutsideAllowlistAndUnderGuard(t *testing.T) {
	t.Parallel()

	path := socketListener(t, func(net.Conn) {})

	result := runSocket(t, context.Background(), &TCPConfig{Socket: path})
	require.Equal(t, checkerdef.StatusError, result.Status)
	require.Contains(t, result.Output[checkerdef.OutputKeyError], checkerdef.LocalSocketSetting)

	ctx := egress.WithGuard(checkerdef.WithLocalSocketAllowlist(context.Background(), []string{path}), egress.New(false))
	result = runSocket(t, ctx, &TCPConfig{Socket: path})
	require.Equal(t, checkerdef.StatusError, result.Status)
	require.Contains(t, result.Output[checkerdef.OutputKeyError], egress.ErrDenied.Error())
}

func TestTCPSocket_MissingFileIsDown(t *testing.T) {
	t.Parallel()

	dir := filepath.Dir(socketListener(t, func(net.Conn) {}))
	missing := filepath.Join(dir, "gone.sock")
	ctx := checkerdef.WithLocalSocketAllowlist(context.Background(), []string{dir + "/"})

	result := runSocket(t, ctx, &TCPConfig{Socket: missing})
	require.Equal(t, checkerdef.StatusDown, result.Status)
	require.Contains(t, result.Output[checkerdef.OutputKeyError], "ENOENT")
}

func TestTCPSocket_Validate(t *testing.T) {
	t.Parallel()

	checker := &TCPChecker{}

	for _, tc := range []struct {
		name   string
		config map[string]any
		field  string
	}{
		{"socket with host", map[string]any{"socket": "/run/app/app.sock", "host": exampleHost}, "socket"},
		{"socket with port", map[string]any{"socket": "/run/app/app.sock", "port": 80}, "socket"},
		{"socket with tls", map[string]any{"socket": "/run/app/app.sock", "tls": true}, "tls"},
		{"socket with url", map[string]any{"socket": "/run/app/app.sock", "url": "tcp://acme.com:80"}, "socket"},
		{"relative socket", map[string]any{"socket": "app.sock"}, "socket"},
		{"tcps url cannot be a socket", map[string]any{"url": "tcps://acme.com:443", "socket": "/run/a.sock"}, "socket"},
	} {
		err := checker.Validate(&checkerdef.CheckSpec{Config: tc.config})
		require.Error(t, err, tc.name)

		var cfgErr *checkerdef.ConfigError
		require.ErrorAs(t, err, &cfgErr, tc.name)
		require.Equal(t, tc.field, cfgErr.Parameter, tc.name)
	}

	// Positive controls: the field form and the URL form both validate.
	spec := &checkerdef.CheckSpec{Config: map[string]any{"socket": "/run/app/app.sock"}}
	require.NoError(t, checker.Validate(spec))
	require.Equal(t, "tcp-run-app-app-sock", spec.Slug)

	cfg := &TCPConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{"url": "unix:///run/app/app.sock"}))
	require.Equal(t, "/run/app/app.sock", cfg.Socket)
	require.NoError(t, checker.Validate(&checkerdef.CheckSpec{Config: map[string]any{"url": "unix:///run/app/app.sock"}}))

	require.Equal(t, map[string]any{"socket": "/run/app/app.sock", "url": "unix:///run/app/app.sock"}, cfg.GetConfig())
}
