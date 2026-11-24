package checkdocker

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// A mounted docker.sock the process cannot open (issue #997: the image runs as
// uid 65532, the socket is root:docker 0660) must say so and name the fix. The
// same socket left open is the positive control: it fails too (nothing speaks
// the Docker API on it), but without the hint.
func TestUnreadableSocketGetsAPermissionHint(t *testing.T) {
	t.Parallel()

	if os.Getuid() == 0 {
		t.Skip("root bypasses socket permissions")
	}

	// A short path: unix socket paths are capped at 104 bytes on macOS.
	dir, err := os.MkdirTemp("", "spdock")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sock := filepath.Join(dir, "docker.sock")

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}

			_ = conn.Close()
		}
	}()

	cfg := &DockerConfig{Host: "unix://" + sock, ContainerName: "web"}

	result, err := (&DockerChecker{}).Execute(t.Context(), cfg)
	require.NoError(t, err)
	require.NotContains(t, result.Output[checkerdef.OutputKeyError], "group_add", "positive control")

	require.NoError(t, os.Chmod(sock, 0o000))

	result, err = (&DockerChecker{}).Execute(t.Context(), cfg)
	require.NoError(t, err)
	require.Equal(t, checkerdef.StatusDown, result.Status)
	require.Contains(t, result.Output[checkerdef.OutputKeyError], "permission denied")
	require.Contains(t, result.Output[checkerdef.OutputKeyError], "group_add")
	require.Contains(t, result.Output[checkerdef.OutputKeyError], "stat -c %g "+sock)
}
