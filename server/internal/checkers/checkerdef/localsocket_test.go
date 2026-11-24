package checkerdef_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
	"github.com/fclairamb/solidping/server/internal/egress"
)

// socketDir returns a short temp directory: a Unix socket path is limited to
// ~104 bytes on macOS, which t.TempDir() under a long test name can exceed.
func socketDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "sps")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return dir
}

// listen opens a Unix listener at dir/name that accepts and counts connections.
func listen(t *testing.T, dir, name string) (string, *atomic.Int32) {
	t.Helper()

	path := filepath.Join(dir, name)
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	accepted := new(atomic.Int32)

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

	return path, accepted
}

func socketCtx(allowlist ...string) context.Context {
	return checkerdef.WithLocalSocketAllowlist(context.Background(), allowlist)
}

func TestDialLocalSocket_AllowedExactAndPrefix(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dir := socketDir(t)
	path, _ := listen(t, dir, "s.sock")

	conn, err := checkerdef.DialLocalSocket(socketCtx(path), path, 0)
	r.NoError(err, "exact entry")
	r.NoError(conn.Close())

	conn, err = checkerdef.DialLocalSocket(socketCtx(dir+"/"), path, 0)
	r.NoError(err, "prefix entry")
	r.NoError(conn.Close())
}

func TestDialLocalSocket_RefusedPathsNeverDial(t *testing.T) {
	t.Parallel()

	dir := socketDir(t)
	path, accepted := listen(t, dir, "s.sock")
	other := socketDir(t)

	for _, tc := range []struct {
		name      string
		allowlist []string
		path      string
		want      error
	}{
		{"empty allowlist", nil, path, checkerdef.ErrLocalSocketNotAllowed},
		{"outside the allowlist", []string{other + "/"}, path, checkerdef.ErrLocalSocketNotAllowed},
		{"dot-dot escape", []string{other + "/"}, other + "/../" + filepath.Base(dir) + "/s.sock",
			checkerdef.ErrLocalSocketPath},
		{"relative path", []string{dir + "/"}, "s.sock", checkerdef.ErrLocalSocketPath},
		{"NUL byte", []string{dir + "/"}, path + "\x00x", checkerdef.ErrLocalSocketPath},
		{"abstract socket", []string{"/"}, "@abstract", checkerdef.ErrLocalSocketPath},
		{"prefix is not an exact entry", []string{dir}, path, checkerdef.ErrLocalSocketNotAllowed},
	} {
		conn, err := checkerdef.DialLocalSocket(socketCtx(tc.allowlist...), tc.path, 0)
		require.ErrorIs(t, err, tc.want, tc.name)
		require.Nil(t, conn, tc.name)
	}

	require.Zero(t, accepted.Load(), "a refused path must never be dialed")

	err := checkerdef.LocalSocketAllowed(nil, path)
	require.ErrorContains(t, err, checkerdef.LocalSocketSetting)
}

func TestDialLocalSocket_EgressGuardAndTunnelRefuse(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dir := socketDir(t)
	path, accepted := listen(t, dir, "s.sock")

	ctx := egress.WithGuard(socketCtx(path), egress.New(false))
	ctx, recorder := egress.WithRecorder(ctx)

	_, err := checkerdef.DialLocalSocket(ctx, path, 0)
	r.ErrorIs(err, egress.ErrDenied)
	r.NotNil(recorder.Denied(), "the refusal must be recorded")

	tunneled := checkerdef.WithTunnelDialer(socketCtx(path), checkerdef.DialerFunc(
		func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("unused") }))
	_, err = checkerdef.DialLocalSocket(tunneled, path, 0)
	r.ErrorIs(err, checkerdef.ErrLocalSocketTunneled)

	r.Zero(accepted.Load())

	// Positive control: a permissive guard lets the same dial through.
	conn, err := checkerdef.DialLocalSocket(egress.WithGuard(socketCtx(path), egress.New(true)), path, 0)
	r.NoError(err)
	r.NoError(conn.Close())
}

func TestDialLocalSocket_ErrnoMessages(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dir := socketDir(t)

	missing := filepath.Join(dir, "missing.sock")
	_, err := checkerdef.DialLocalSocket(socketCtx(dir+"/"), missing, 0)
	r.ErrorIs(err, syscall.ENOENT)
	r.ErrorContains(err, "ENOENT")

	// A closed listener leaves its file behind when unlink-on-close is off.
	stale := filepath.Join(dir, "stale.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: stale, Net: "unix"})
	r.NoError(err)
	listener.SetUnlinkOnClose(false)
	r.NoError(listener.Close())

	_, err = checkerdef.DialLocalSocket(socketCtx(dir+"/"), stale, 0)
	r.ErrorIs(err, syscall.ECONNREFUSED)
	r.ErrorContains(err, "ECONNREFUSED")

	if os.Geteuid() == 0 {
		t.Log("running as root: the EACCES case cannot be observed")

		return
	}

	locked, _ := listen(t, dir, "locked.sock")
	r.NoError(os.Chmod(locked, 0o000))

	_, err = checkerdef.DialLocalSocket(socketCtx(dir+"/"), locked, 0)
	r.ErrorIs(err, syscall.EACCES)
	r.ErrorContains(err, "EACCES")
}

func TestSocketHTTPTransport_DialsTheSocket(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dir := socketDir(t)
	path, _ := listen(t, dir, "s.sock")

	transport := checkerdef.SocketHTTPTransport(socketCtx(path), path)
	conn, err := transport.DialContext(context.Background(), "tcp", "ignored.acme.com:80")
	r.NoError(err)
	r.NoError(conn.Close())

	transport = checkerdef.SocketHTTPTransport(socketCtx(), path)
	_, err = transport.DialContext(context.Background(), "tcp", "ignored.acme.com:80")
	r.ErrorIs(err, checkerdef.ErrLocalSocketNotAllowed)
}

// TestSupportsLocalSocketMatchesConfigContract keeps the capability flag and
// the config contract in step: a type's catalog entry says it supports a
// socket exactly when its config implements LocalSocketTarget.
func TestSupportsLocalSocketMatchesConfigContract(t *testing.T) {
	t.Parallel()

	metas := checkerdef.ListCheckTypeMetas()
	require.NotEmpty(t, metas)

	for idx := range metas {
		cfg, ok := configregistry.ParseConfig(metas[idx].Type)
		if !ok {
			continue
		}

		_, implements := cfg.(checkerdef.LocalSocketTarget)
		require.Equal(t, implements, metas[idx].SupportsLocalSocket, "type %s", metas[idx].Type)
	}
}

func TestIsHostLocal(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.True(checkerdef.IsHostLocal(checkerdef.CheckTypeDocker, nil))
	r.False(checkerdef.IsHostLocal(checkerdef.CheckTypeTCP, nil))

	cfg, _ := configregistry.ParseConfig(checkerdef.CheckTypeTCP)
	r.NoError(cfg.FromMap(map[string]any{"host": "acme.com", "port": 80}))
	r.False(checkerdef.IsHostLocal(checkerdef.CheckTypeTCP, cfg))

	cfg, _ = configregistry.ParseConfig(checkerdef.CheckTypeTCP)
	r.NoError(cfg.FromMap(map[string]any{"socket": "/run/app/app.sock"}))
	r.True(checkerdef.IsHostLocal(checkerdef.CheckTypeTCP, cfg))
}
