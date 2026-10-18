package finger

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
	"github.com/fclairamb/solidping/server/internal/servicesig"
)

// fakeFinger serves one exchange per connection on 127.0.0.1: it reads the
// query line, then lets handle answer.
func fakeFinger(t *testing.T, handle func(conn net.Conn, query string)) float64 {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
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

				line, _ := bufio.NewReader(conn).ReadString('\n')
				handle(conn, strings.TrimRight(line, "\r\n"))
			}()
		}
	}()

	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)

	return float64(addr.Port)
}

func runWithin(t *testing.T, budget time.Duration, config map[string]any) protocol.ExecuteResponse {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()

	return Run(ctx, config)
}

func TestFingerAnswerIsUp(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	queries := make(chan string, 1)

	port := fakeFinger(t, func(conn net.Conn, query string) {
		queries <- query

		_, _ = io.WriteString(conn, "  Login: alice   Name: Alice\r\nDirectory: /home/alice\r\nNo plan.\r\n")
	})

	res := runWithin(t, 5*time.Second, map[string]any{"host": "127.0.0.1", "port": port, "user": "alice"})
	r.Equal(statusUp, res.Status, res.Output)
	r.Equal(3, res.Metrics["lines"])
	r.Positive(res.Metrics["bytes"])
	r.Equal("Login: alice   Name: Alice", res.Output["message"])
	r.Equal("alice", <-queries)
}

func TestFingerContainsAbsentIsDown(t *testing.T) {
	t.Parallel()

	port := fakeFinger(t, func(conn net.Conn, _ string) {
		_, _ = io.WriteString(conn, "No one logged on.\r\n")
	})

	res := runWithin(t, 5*time.Second, map[string]any{"host": "127.0.0.1", "port": port, "contains": "alice"})
	require.Equal(t, statusDown, res.Status, res.Output)
}

func TestFingerEmptyAnswerIsDown(t *testing.T) {
	t.Parallel()

	port := fakeFinger(t, func(net.Conn, string) {})

	res := runWithin(t, 5*time.Second, map[string]any{"host": "127.0.0.1", "port": port})
	require.Equal(t, statusDown, res.Status, res.Output)
}

func TestFingerClosedPortIsDown(t *testing.T) {
	t.Parallel()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	require.NoError(t, listener.Close())

	res := runWithin(t, 5*time.Second, map[string]any{"host": "127.0.0.1", "port": float64(addr.Port)})
	require.Equal(t, statusDown, res.Status, res.Output)
}

func TestFingerSilentServerTimesOut(t *testing.T) {
	t.Parallel()

	port := fakeFinger(t, func(net.Conn, string) { time.Sleep(3 * time.Second) })

	res := runWithin(t, 300*time.Millisecond, map[string]any{"host": "127.0.0.1", "port": port})
	require.Equal(t, statusTimeout, res.Status, res.Output)
}

func TestFingerValidateRefusesMultilineUser(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Empty(Validate(map[string]any{"user": "alice"}))
	r.Len(Validate(map[string]any{"user": "alice\r\nbob"}), 1)
}

func TestFingerHandlerChecksSignature(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	keys := servicesig.KeySet{{ID: "2026-10", Secret: "acme-secret"}}
	server := httptest.NewServer(NewHandler(keys, nil))
	t.Cleanup(server.Close)

	send := func(method, path string, signWith servicesig.KeySet) (int, string) {
		body := []byte(`{}`)
		if method == http.MethodGet {
			body = nil
		}

		req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, bytes.NewReader(body))
		r.NoError(err)

		if signWith != nil {
			r.NoError(servicesig.SignRequest(req, signWith, body, time.Now()))
		}

		resp, err := http.DefaultClient.Do(req)
		r.NoError(err)

		defer func() { _ = resp.Body.Close() }()

		raw, err := io.ReadAll(resp.Body)
		r.NoError(err)

		return resp.StatusCode, string(raw)
	}

	status, _ := send(http.MethodGet, protocol.PathCheckers, nil)
	r.Equal(http.StatusUnauthorized, status, "an unsigned request gets a 401")

	status, body := send(http.MethodGet, protocol.PathCheckers, keys)
	r.Equal(http.StatusOK, status, "positive control: a signed request is served")
	r.Contains(body, `"x-finger"`)

	// A request signed with another key is refused too.
	status, _ = send(http.MethodPost, protocol.PathExecute, servicesig.KeySet{{ID: "2026-10", Secret: "other"}})
	r.Equal(http.StatusUnauthorized, status)
}
