package checkredis_test

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkredis"
)

// respServer is a minimal RESP server on a Unix socket: PING is +PONG, every
// other command (HELLO, CLIENT...) an error go-redis tolerates. It returns the
// socket path (short directory: macOS caps socket paths at ~104 bytes).
func respServer(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "rds")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "r.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	require.NoError(t, err)

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			go serveRESP(conn)
		}
	}()

	return path
}

func serveRESP(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)

	for {
		header, err := reader.ReadString('\n')
		if err != nil || !strings.HasPrefix(header, "*") {
			return
		}

		count, _ := strconv.Atoi(strings.TrimSpace(header[1:]))
		args := make([]string, 0, count)

		for range count {
			sizeLine, readErr := reader.ReadString('\n')
			if readErr != nil {
				return
			}

			size, _ := strconv.Atoi(strings.TrimSpace(sizeLine[1:]))
			buf := make([]byte, size+2)

			if _, readErr = reader.Read(buf[:0]); readErr != nil {
				return
			}

			for got := 0; got < len(buf); {
				n, readErr := reader.Read(buf[got:])
				if readErr != nil {
					return
				}

				got += n
			}

			args = append(args, string(buf[:size]))
		}

		reply := "-ERR unknown command\r\n"
		if len(args) > 0 && strings.EqualFold(args[0], "ping") {
			reply = "+PONG\r\n"
		}

		if _, err := conn.Write([]byte(reply)); err != nil {
			return
		}
	}
}

func runRedisSocket(t *testing.T, path string, allow []string) *checkerdef.Result {
	t.Helper()

	cfg := &checkredis.RedisConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{"socket": path, "timeout": "3s"}))

	ctx := checkerdef.WithLocalSocketAllowlist(t.Context(), allow)

	result, err := (&checkredis.RedisChecker{}).Execute(ctx, cfg)
	require.NoError(t, err)

	return result
}

func TestRedisSocketPing(t *testing.T) {
	t.Parallel()

	path := respServer(t)
	allow := []string{filepath.Dir(path) + "/"}

	t.Run("allowlisted socket is up", func(t *testing.T) {
		t.Parallel()

		result := runRedisSocket(t, path, allow)
		require.Equal(t, checkerdef.StatusUp, result.Status, result.Output)
		require.Equal(t, path, result.Output["socket"])
		require.Equal(t, "PONG", result.Output["result"])
	})

	t.Run("missing socket is down with ENOENT", func(t *testing.T) {
		t.Parallel()

		missing := filepath.Join(filepath.Dir(path), "missing.sock")
		result := runRedisSocket(t, missing, allow)
		require.Equal(t, checkerdef.StatusDown, result.Status, result.Output)
		require.Contains(t, result.Output[checkerdef.OutputKeyError], "ENOENT")
	})

	t.Run("path outside the allowlist is refused without dialing", func(t *testing.T) {
		t.Parallel()

		result := runRedisSocket(t, path, []string{"/run/other/"})
		require.NotEqual(t, checkerdef.StatusUp, result.Status)
		require.Contains(t, result.Output[checkerdef.OutputKeyError], checkerdef.LocalSocketSetting)

		result = runRedisSocket(t, path, nil)
		require.NotEqual(t, checkerdef.StatusUp, result.Status, "an empty allowlist refuses every path")
	})
}

func TestRedisSocketConfig(t *testing.T) {
	t.Parallel()

	t.Run("socket with host is rejected", func(t *testing.T) {
		t.Parallel()

		spec := &checkerdef.CheckSpec{Config: map[string]any{"socket": "/run/redis/redis.sock", "host": "acme.com"}}
		require.ErrorContains(t, (&checkredis.RedisChecker{}).Validate(spec), "mutually exclusive")
	})

	t.Run("socket alone is valid and named", func(t *testing.T) {
		t.Parallel()

		spec := &checkerdef.CheckSpec{Config: map[string]any{"socket": "/run/redis/redis.sock"}}
		require.NoError(t, (&checkredis.RedisChecker{}).Validate(spec))
		require.Equal(t, "redis-run-redis-redis-sock", spec.Slug)
	})

	t.Run("relative path is rejected", func(t *testing.T) {
		t.Parallel()

		spec := &checkerdef.CheckSpec{Config: map[string]any{"socket": "redis.sock"}}
		require.Error(t, (&checkredis.RedisChecker{}).Validate(spec))
	})

	t.Run("redis+unix url expands to socket and database", func(t *testing.T) {
		t.Parallel()

		cfg := &checkredis.RedisConfig{}
		require.NoError(t, cfg.FromMap(map[string]any{"url": "redis+unix:///run/redis/redis.sock?db=3"}))
		require.Equal(t, "/run/redis/redis.sock", cfg.Socket)
		require.Equal(t, 3, cfg.Database)
		require.Equal(t, "/run/redis/redis.sock", cfg.GetConfig()["socket"])
	})
}

// TestRedisPathHostNeverDialsSocket: go-redis dials `unix` for an address
// starting with `/`; the host:port path refuses it (spec 2026-11-24-08).
func TestRedisPathHostNeverDialsSocket(t *testing.T) {
	t.Parallel()

	dir, err := os.MkdirTemp("", "rdh")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// Addr() is "<host>:<port>": name the socket file so that it matches.
	sock := filepath.Join(dir, "r")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", sock+":6379")
	require.NoError(t, err)

	t.Cleanup(func() { _ = listener.Close() })

	accepted := make(chan struct{}, 1)

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}

		accepted <- struct{}{}
		_ = conn.Close()
	}()

	cfg := &checkredis.RedisConfig{Host: sock, Port: 6379}

	result, err := (&checkredis.RedisChecker{}).Execute(t.Context(), cfg)
	require.NoError(t, err)
	require.NotEqual(t, checkerdef.StatusUp, result.Status)
	require.Contains(t, result.Output[checkerdef.OutputKeyError], checkerdef.LocalSocketSetting)
	require.Empty(t, accepted, "a path host must never reach the socket")
}
