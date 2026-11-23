package checkunixsocket

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestConnectOnly_Up(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	path := listen(t, func(conn net.Conn) { _ = conn.Close() })

	result := run(t, t.Context(), map[string]any{"path": path})
	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)
	r.Equal(path, result.Output[OutputKeyPath])
	r.Equal("connect", result.Output[OutputKeyMode])
	r.Contains(result.Metrics, metricConnectTime)
}

func TestConnectOnly_MissingFileIsDownWithENOENT(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := run(t, t.Context(), map[string]any{"path": socketPath(t)})
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal("ENOENT", result.Output[OutputKeyErrno])
	r.Contains(result.Output[checkerdef.OutputKeyError], "socket file not found")
}

func TestConnectOnly_NobodyListeningIsDownWithECONNREFUSED(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// A closed listener can leave its file behind: the file exists, nobody
	// accepts on it.
	path := socketPath(t)
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	r.NoError(err)

	unixLn, ok := ln.(*net.UnixListener)
	r.True(ok)
	unixLn.SetUnlinkOnClose(false)
	r.NoError(ln.Close())

	_, statErr := os.Stat(path)
	r.NoError(statErr, "the socket file must survive the closed listener")

	result := run(t, t.Context(), map[string]any{"path": path})
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal("ECONNREFUSED", result.Output[OutputKeyErrno])
}

func TestConnectOnly_PlainFileIsDown(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	path := filepath.Join(filepath.Dir(socketPath(t)), "plain")
	r.NoError(os.WriteFile(path, []byte("not a socket"), 0o600))

	result := run(t, t.Context(), map[string]any{"path": path})
	r.Equal(checkerdef.StatusDown, result.Status)
	r.NotEmpty(result.Output[OutputKeyErrno], "output: %v", result.Output)
}

func TestConnectOnly_PermissionDeniedIsDownWithEACCES(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}

	r := require.New(t)

	path := listen(t, func(conn net.Conn) { _ = conn.Close() })
	r.NoError(os.Chmod(filepath.Dir(path), 0o000))
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o700) })

	result := run(t, t.Context(), map[string]any{"path": path})
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal("EACCES", result.Output[OutputKeyErrno])
}

func echoUpper(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return
	}

	_, _ = fmt.Fprintf(conn, "PONG %s", line)
}

func TestRaw_ExpectMatchIsUp(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	path := listen(t, echoUpper)

	result := run(t, t.Context(), map[string]any{
		"path": path, "send_data": `ping\n`, "send_encoding": "escaped", "expect_data": "PONG",
	})
	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)
	r.Equal("raw", result.Output[OutputKeyMode])
}

func TestRaw_ExpectMismatchIsDown(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	path := listen(t, echoUpper)

	result := run(t, t.Context(), map[string]any{
		"path": path, "send_data": `ping\n`, "send_encoding": "escaped", "expect_data": "HELLO",
	})
	r.Equal(checkerdef.StatusDown, result.Status, "output: %v", result.Output)
}

// serveHTTP serves handler over a fresh Unix socket.
func serveHTTP(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()

	path := socketPath(t)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	require.NoError(t, err)

	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(ln) }()

	t.Cleanup(func() { _ = server.Close() })

	return path
}

func TestHTTP(t *testing.T) {
	t.Parallel()

	path := serveHTTP(t, func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/healthcheck":
			if req.Host != "localhost" {
				w.WriteHeader(http.StatusBadRequest)

				return
			}

			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/broken":
			w.WriteHeader(http.StatusInternalServerError)
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		}
	})

	cases := []struct {
		name   string
		cfg    map[string]any
		status checkerdef.Status
		code   int
	}{
		{"200 is up", map[string]any{"http_path": "/healthcheck"}, checkerdef.StatusUp, 200},
		{"500 is down", map[string]any{"http_path": "/broken"}, checkerdef.StatusDown, 500},
		{"body match is up", map[string]any{"http_path": "/healthcheck", "body_expect": `"ok"`}, checkerdef.StatusUp, 200},
		{"body miss is down", map[string]any{"http_path": "/healthcheck", "body_expect": "nope"}, checkerdef.StatusDown, 200},
		{"expected 404 is up", map[string]any{"http_path": "/missing", "expected_status": 404}, checkerdef.StatusUp, 404},
		{"expected 404 on 200 is down", map[string]any{"http_path": "/healthcheck", "expected_status": 404}, checkerdef.StatusDown, 200},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			tc.cfg["path"] = path
			result := run(t, t.Context(), tc.cfg)
			r.Equal(tc.status, result.Status, "output: %v", result.Output)
			r.Equal(tc.code, result.Metrics[metricStatusCode])
			r.Contains(result.Metrics, metricResponseTime)
			r.Contains(result.Metrics, metricConnectTime)
			r.Equal("http", result.Output[OutputKeyMode])
		})
	}
}

func TestHTTP_MissingSocketIsDownWithENOENT(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := run(t, t.Context(), map[string]any{"path": socketPath(t), "http_path": "/"})
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal("ENOENT", result.Output[OutputKeyErrno])
}

// silent accepts and never answers, holding the connection until the test ends.
func silent(t *testing.T) func(net.Conn) {
	t.Helper()

	return func(conn net.Conn) {
		<-t.Context().Done()
		_ = conn.Close()
	}
}

func TestTimeout_RawSilentListener(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	path := listen(t, silent(t))

	result := run(t, t.Context(), map[string]any{
		"path": path, "timeout": "200ms", "send_data": "ping", "expect_data": "pong",
	})
	r.Equal(checkerdef.StatusTimeout, result.Status, "output: %v", result.Output)
}

func TestTimeout_HTTPSilentListener(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	path := listen(t, silent(t))

	result := run(t, t.Context(), map[string]any{"path": path, "timeout": "200ms", "http_path": "/"})
	r.Equal(checkerdef.StatusTimeout, result.Status, "output: %v", result.Output)
}

func TestType(t *testing.T) {
	t.Parallel()
	require.Equal(t, checkerdef.CheckTypeUnixSocket, (&UnixSocketChecker{}).Type())
}
