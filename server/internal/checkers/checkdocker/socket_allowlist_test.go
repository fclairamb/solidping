package checkdocker

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// fakeSocketDaemon listens on a Unix socket in a fresh temp dir and answers
// every request like a Docker daemon's /_ping. It returns the socket path and
// a counter of accepted connections.
func fakeSocketDaemon(t *testing.T) (string, *atomic.Int32) {
	t.Helper()

	// A short path: unix socket paths are capped at 104 bytes on macOS.
	dir, err := os.MkdirTemp("", "spdsk")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sock := filepath.Join(dir, "other.sock")

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", sock)
	require.NoError(t, err)

	var accepted atomic.Int32

	srv := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Api-Version", "1.45")
			w.Header().Set("Ostype", "linux")
			_, _ = w.Write([]byte("OK"))
		}),
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				accepted.Add(1)
			}
		},
	}

	go func() { _ = srv.Serve(ln) }()

	t.Cleanup(func() { _ = srv.Close() })

	return sock, &accepted
}

// A unix:// docker host outside checkers.docker_socket_paths is refused before
// any dial (spec 2026-11-27-03); the same socket, once listed, is dialed.
func TestDockerSocketAllowlist(t *testing.T) {
	t.Parallel()

	t.Run("unlisted socket is refused without a dial", func(t *testing.T) {
		t.Parallel()

		sock, accepted := fakeSocketDaemon(t)
		cfg := &DockerConfig{Host: "unix://" + sock, Timeout: time.Second}

		// No allowlist on the context: the default list applies.
		result, err := (&DockerChecker{}).Execute(t.Context(), cfg)
		require.NoError(t, err)
		require.Equal(t, checkerdef.StatusDown, result.Status, "%v", result.Output)
		require.Contains(t, result.Output[checkerdef.OutputKeyError], checkerdef.DockerSocketSetting)

		// A list naming another socket.
		ctx := checkerdef.WithDockerSocketAllowlist(t.Context(), []string{"/var/run/docker.sock"})
		result, err = (&DockerChecker{}).Execute(ctx, cfg)
		require.NoError(t, err)
		require.Equal(t, checkerdef.StatusDown, result.Status, "%v", result.Output)

		// An empty list turns docker sockets off.
		ctx = checkerdef.WithDockerSocketAllowlist(t.Context(), []string{})
		result, err = (&DockerChecker{}).Execute(ctx, cfg)
		require.NoError(t, err)
		require.Equal(t, checkerdef.StatusDown, result.Status, "%v", result.Output)
		require.Contains(t, result.Output[checkerdef.OutputKeyError], "off")

		// Container mode is refused the same way.
		cfg.ContainerName = "web"
		result, err = (&DockerChecker{}).Execute(t.Context(), cfg)
		require.NoError(t, err)
		require.Equal(t, checkerdef.StatusDown, result.Status, "%v", result.Output)

		require.Zero(t, accepted.Load(), "a refused socket must never be dialed")
	})

	t.Run("listed socket is dialed", func(t *testing.T) {
		t.Parallel()

		sock, accepted := fakeSocketDaemon(t)
		cfg := &DockerConfig{Host: "unix://" + sock, Timeout: time.Second}

		for _, allow := range [][]string{{sock}, {filepath.Dir(sock) + "/"}} {
			ctx := checkerdef.WithDockerSocketAllowlist(t.Context(), allow)

			result, err := (&DockerChecker{}).Execute(ctx, cfg)
			require.NoError(t, err)
			require.Equal(t, checkerdef.StatusUp, result.Status, "%v", result.Output)
			require.Equal(t, "1.45", result.Output["apiVersion"])
		}

		require.Positive(t, accepted.Load())
	})

	t.Run("default host passes the default list", func(t *testing.T) {
		t.Parallel()

		cfg := &DockerConfig{}
		_, path, ok := checkerdef.DockerSocketOf(cfg)
		require.True(t, ok)
		require.Equal(t, "/var/run/docker.sock", path)
		require.NoError(t, checkerdef.DockerSocketAllowed(checkerdef.DockerSocketAllowlistFrom(t.Context()), path))
		require.NoError(t, checkerdef.DockerSocketAllowed(checkerdef.DefaultDockerSocketPaths(), "/run/docker.sock"))

		_, _, ok = checkerdef.DockerSocketOf(&DockerConfig{Host: "tcp://10.0.0.1:2375"})
		require.False(t, ok, "a tcp host names no socket")
	})
}
