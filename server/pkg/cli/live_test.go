package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
	"go.uber.org/goleak"
)

// lockedBuffer is a goroutine-safe bytes.Buffer.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// liveServer speaks the org live socket protocol.
type liveServer struct {
	srv       *httptest.Server
	conns     chan *websocket.Conn
	subscribe chan string
	closed    chan websocket.StatusCode
	auth      atomic.Value
}

func newLiveServer(t *testing.T) *liveServer {
	t.Helper()

	ls := &liveServer{
		conns:     make(chan *websocket.Conn, 10),
		subscribe: make(chan string, 10),
		closed:    make(chan websocket.StatusCode, 10),
	}
	ls.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ls.auth.Store(r.Header.Get("Authorization"))

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"solidping.v2"}})
		if err != nil {
			return
		}

		ctx := r.Context()
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"hello","protocol":2}`))

		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}

		var msg struct {
			Type   string `json:"type"`
			Entity string `json:"entity"`
		}
		_ = json.Unmarshal(data, &msg)
		ls.subscribe <- msg.Type + ":" + msg.Entity

		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"subscribed","entity":"`+msg.Entity+`"}`))

		ls.conns <- conn

		// Keep the handler alive until the client goes away.
		for {
			if _, _, err = conn.Read(ctx); err != nil {
				ls.closed <- websocket.CloseStatus(err)

				return
			}
		}
	}))
	t.Cleanup(ls.srv.Close)

	return ls
}

func (ls *liveServer) next(t *testing.T) *websocket.Conn {
	t.Helper()

	select {
	case conn := <-ls.conns:
		return conn
	case <-time.After(5 * time.Second):
		t.Fatal("no live connection")

		return nil
	}
}

func testWatchConfig(url string, refreshes *atomic.Int32, out *lockedBuffer) *watchConfig {
	return &watchConfig{
		Title:   "checks list",
		BaseURL: url,
		Org:     "acme",
		Entity:  "checks",
		Token:   func(context.Context) (string, error) { return "tok", nil },
		Refresh: func(context.Context) error {
			refreshes.Add(1)

			return nil
		},
		Out:          out,
		Debounce:     50 * time.Millisecond,
		PollInterval: 40 * time.Millisecond,
		BackoffStart: 10 * time.Millisecond,
		BackoffMax:   20 * time.Millisecond,
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	require.Eventually(t, cond, 5*time.Second, 5*time.Millisecond)
}

func startWatch(t *testing.T, cfg *watchConfig) (context.CancelFunc, <-chan error) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- runWatch(ctx, cfg) }()

	return cancel, done
}

func stopWatch(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not stop")
	}
}

//nolint:paralleltest // goleak inspects every goroutine of the process
func TestWatchSubscribesAndRefetchesOnHints(t *testing.T) {
	ignore := goleak.IgnoreCurrent()
	ls := newLiveServer(t)

	var refreshes atomic.Int32

	out := &lockedBuffer{}
	cancel, done := startWatch(t, testWatchConfig(ls.srv.URL, &refreshes, out))

	conn := ls.next(t)
	require.Equal(t, "subscribe:checks", <-ls.subscribe)
	require.Equal(t, "Bearer tok", ls.auth.Load())

	// Initial draw after subscribe.
	waitFor(t, func() bool { return refreshes.Load() == 1 })
	require.Contains(t, out.String(), "live")

	// One update -> one refetch.
	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte(`{"type":"update","entity":"checks"}`)))
	waitFor(t, func() bool { return refreshes.Load() == 2 })

	// One resync -> one refetch.
	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte(`{"type":"resync"}`)))
	waitFor(t, func() bool { return refreshes.Load() == 3 })

	// A burst inside the debounce window -> a single refetch.
	for range 5 {
		require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte(`{"type":"update","entity":"checks"}`)))
	}
	waitFor(t, func() bool { return refreshes.Load() == 4 })
	time.Sleep(200 * time.Millisecond)
	require.EqualValues(t, 4, refreshes.Load())

	stopWatch(t, cancel, done)

	// No goroutine of ours (reader, debounce) survives the cancellation.
	ls.srv.Close()
	goleak.VerifyNone(t, ignore)
}

func TestWatchReconnectsAfterServerClose(t *testing.T) {
	t.Parallel()

	ls := newLiveServer(t)

	var refreshes atomic.Int32

	cancel, done := startWatch(t, testWatchConfig(ls.srv.URL, &refreshes, &lockedBuffer{}))

	conn := ls.next(t)
	waitFor(t, func() bool { return refreshes.Load() == 1 })

	require.NoError(t, conn.Close(websocket.StatusGoingAway, "restart"))

	// Reconnect (a second subscribe) and refetch.
	ls.next(t)
	waitFor(t, func() bool { return refreshes.Load() == 2 })

	stopWatch(t, cancel, done)
}

func TestWatchFallsBackToPollingWhenHandshakeRejected(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))

		var refreshes atomic.Int32

		out := &lockedBuffer{}
		cancel, done := startWatch(t, testWatchConfig(srv.URL, &refreshes, out))

		waitFor(t, func() bool { return refreshes.Load() >= 3 })
		require.Contains(t, out.String(), "polling")
		require.NotContains(t, out.String(), "| live")

		stopWatch(t, cancel, done)
		srv.Close()
	}
}

func TestWatchCancelClosesSocket(t *testing.T) {
	t.Parallel()

	ls := newLiveServer(t)

	var refreshes atomic.Int32

	cancel, done := startWatch(t, testWatchConfig(ls.srv.URL, &refreshes, &lockedBuffer{}))

	ls.next(t)
	waitFor(t, func() bool { return refreshes.Load() == 1 })

	stopWatch(t, cancel, done)

	// The server side observes a clean close.
	select {
	case status := <-ls.closed:
		require.Equal(t, websocket.StatusNormalClosure, status)
	case <-time.After(5 * time.Second):
		t.Fatal("socket not closed")
	}
}

//nolint:paralleltest // t.Setenv
func TestWatchRejectsJSONOutput(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	for _, sub := range [][]string{{"checks", "list"}, {"incidents", "list"}} {
		t.Setenv("HOME", t.TempDir())

		root := &cli.Command{Name: "sp", Flags: GetGlobalFlags(), Commands: GetCommands()}
		args := append([]string{"sp", "--url", srv.URL, "-o", "json"}, sub...)
		err := root.Run(t.Context(), append(args, "--watch"))
		require.ErrorIs(t, err, ErrWatchJSONUnsupported)
		require.Contains(t, err.Error(), "jsonl")
	}

	require.Zero(t, calls.Load(), "no network call expected")
}

func TestWatchJSONLWritesOneSnapshotLinePerRefresh(t *testing.T) {
	t.Parallel()

	ls := newLiveServer(t)

	var refreshes atomic.Int32

	out := &lockedBuffer{}
	errOut := &lockedBuffer{}
	cfg := testWatchConfig(ls.srv.URL, &refreshes, out)
	cfg.JSONL = true
	cfg.Err = errOut
	cfg.Fetch = func(context.Context) (json.RawMessage, error) {
		n := refreshes.Add(1)
		if n == 3 {
			return nil, context.DeadlineExceeded
		}

		return json.RawMessage(`[{"slug":"a"}]`), nil
	}

	cancel, done := startWatch(t, cfg)

	conn := ls.next(t)
	waitFor(t, func() bool { return refreshes.Load() == 1 })

	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte(`{"type":"update"}`)))
	waitFor(t, func() bool { return refreshes.Load() == 2 })

	// A failing refresh writes nothing on stdout, a notice on stderr.
	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte(`{"type":"update"}`)))
	waitFor(t, func() bool { return refreshes.Load() == 3 })
	waitFor(t, func() bool { return errOut.String() != "" })

	stopWatch(t, cancel, done)

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	require.Len(t, lines, 2)

	for _, line := range lines {
		var snap struct {
			RefreshedAt string           `json:"refreshedAt"`
			Source      string           `json:"source"`
			Data        []map[string]any `json:"data"`
		}

		require.NoError(t, json.Unmarshal([]byte(line), &snap))
		_, err := time.Parse(time.RFC3339, snap.RefreshedAt)
		require.NoError(t, err)
		require.Equal(t, "live", snap.Source)
		require.Len(t, snap.Data, 1)
	}

	require.NotContains(t, out.String(), "\x1b")
	require.Contains(t, errOut.String(), "refresh failed")
}

func TestWatchJSONLPollingNoticeGoesToStderr(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	var refreshes atomic.Int32

	out := &lockedBuffer{}
	errOut := &lockedBuffer{}
	cfg := testWatchConfig(srv.URL, &refreshes, out)
	cfg.JSONL = true
	cfg.Err = errOut
	cfg.Fetch = func(context.Context) (json.RawMessage, error) {
		refreshes.Add(1)

		return json.RawMessage(`[]`), nil
	}

	cancel, done := startWatch(t, cfg)

	waitFor(t, func() bool { return refreshes.Load() >= 2 })
	stopWatch(t, cancel, done)

	require.Contains(t, errOut.String(), "polling")
	require.Contains(t, out.String(), `"source":"polling"`)
	require.NotContains(t, out.String(), "polling every")
}
