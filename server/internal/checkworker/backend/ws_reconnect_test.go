package backend_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
)

// fakeAgentServer is a controllable stand-in for the agentws handler: it speaks
// just enough of the protocol to enroll/reconnect a WSBackend, then hands each
// post-handshake connection to a per-connection hook so a test can orchestrate
// drops, silence (half-open), and hint injection precisely.
type fakeAgentServer struct {
	server *httptest.Server
	region string

	mu     sync.Mutex
	conns  int
	claims int
	onConn func(idx int, conn *websocket.Conn)
}

func newFakeAgentServer(t *testing.T, region string) *fakeAgentServer {
	t.Helper()

	fake := &fakeAgentServer{region: region}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)

	return fake
}

// handle installs the per-connection hook (guarded so the race detector sees a
// clean happens-before with the reads in serve).
func (f *fakeAgentServer) handle(fn func(idx int, conn *websocket.Conn)) {
	f.mu.Lock()
	f.onConn = fn
	f.mu.Unlock()
}

func (f *fakeAgentServer) claimCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.claims
}

func (f *fakeAgentServer) serve(writer http.ResponseWriter, req *http.Request) {
	conn, err := websocket.Accept(writer, req, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}

	conn.SetReadLimit(256 * 1024)

	isEnroll := req.Header.Get("Authorization") != ""
	if !f.handshake(req.Context(), conn, isEnroll) {
		return
	}

	f.mu.Lock()
	idx := f.conns
	f.conns++
	onConn := f.onConn
	f.mu.Unlock()

	if onConn != nil {
		onConn(idx, conn)
	}
}

// handshake replays the server side of enrollment (hello → enrolled → identity
// hello) or a signed reconnect (a single hello). Signatures are not checked;
// the client-side behavior under test is the connection lifecycle, not auth.
func (f *fakeAgentServer) handshake(ctx context.Context, conn *websocket.Conn, isEnroll bool) bool {
	now := time.Now().UTC().Format(time.RFC3339)

	if !isEnroll {
		return wsjson.Write(ctx, conn, agents.ServerFrame{
			Type: agents.MsgTypeHello, AgentUID: "agent-1", WorkerUID: "worker-1", Region: f.region,
		}) == nil
	}

	if err := wsjson.Write(ctx, conn, agents.ServerFrame{
		Type: agents.MsgTypeHello, Protocol: agents.ProtocolVersion, ServerTime: now,
	}); err != nil {
		return false
	}

	var enroll agents.ClientFrame
	if err := wsjson.Read(ctx, conn, &enroll); err != nil {
		return false
	}

	if err := wsjson.Write(ctx, conn, agents.ServerFrame{
		Type: agents.MsgTypeEnrolled, ID: enroll.ID,
		AgentUID: "agent-1", WorkerUID: "worker-1", Region: f.region, Fingerprint: "fp-1",
	}); err != nil {
		return false
	}

	return wsjson.Write(ctx, conn, agents.ServerFrame{
		Type: agents.MsgTypeHello, AgentUID: "agent-1", WorkerUID: "worker-1", Region: f.region,
	}) == nil
}

// serveReads is the healthy-connection behavior: read frames until the conn
// closes, counting claims and acking whatever arrives. Actively reading also
// lets the websocket library answer the agent's keepalive pings with pongs.
func (f *fakeAgentServer) serveReads(conn *websocket.Conn) {
	for {
		var frame agents.ClientFrame
		if err := wsjson.Read(context.Background(), conn, &frame); err != nil {
			return
		}

		switch frame.Type {
		case agents.MsgTypeClaim:
			f.mu.Lock()
			f.claims++
			f.mu.Unlock()

			_ = wsjson.Write(context.Background(), conn, agents.ServerFrame{Type: agents.MsgTypeJobs, ID: frame.ID})
		case agents.MsgTypeResult:
			_ = wsjson.Write(context.Background(), conn, agents.ServerFrame{Type: agents.MsgTypeAck, ID: frame.ID})
		default:
		}
	}
}

// newWSBackend builds a WSBackend pointed at the fake server with a fresh agent
// identity.
func newWSBackend(t *testing.T, fake *fakeAgentServer, opts ...backend.Option) *backend.WSBackend {
	t.Helper()

	keys, err := agents.GenerateAgentKeys()
	require.NoError(t, err)

	return backend.NewWSBackend(
		fake.server.URL, "spe_token", "agent",
		&backend.Identity{AgentKeys: *keys}, nil, opts...,
	)
}

// logSink captures slog records for assertions.
type logSink struct {
	mu      sync.Mutex
	entries []logEntry
}

type logEntry struct {
	level slog.Level
	msg   string
}

type capturingHandler struct {
	sink *logSink
}

func newCapturingHandler() *capturingHandler {
	return &capturingHandler{sink: &logSink{}}
}

func (h *capturingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, record slog.Record) error {
	h.sink.mu.Lock()
	h.sink.entries = append(h.sink.entries, logEntry{level: record.Level, msg: record.Message})
	h.sink.mu.Unlock()

	return nil
}

func (h *capturingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }

func (h *capturingHandler) WithGroup(_ string) slog.Handler { return h }

func (h *capturingHandler) has(level slog.Level, msg string) bool {
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()

	for _, entry := range h.sink.entries {
		if entry.level == level && entry.msg == msg {
			return true
		}
	}

	return false
}

// TestWSBackendReconnectsAfterServerClose: when the server closes the
// connection, the agent logs the loss at Warn and re-establishes on its own,
// without issuing any claim, and a jobs-available hint sent on the reconnected
// connection still reaches Hints() subscribers (the express path survives).
func TestWSBackendReconnectsAfterServerClose(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	logs := newCapturingHandler()
	fake := newFakeAgentServer(t, "@acme/dc1")

	closeFirst := make(chan struct{})

	fake.handle(func(idx int, conn *websocket.Conn) {
		if idx == 0 {
			<-closeFirst
			_ = conn.Close(websocket.StatusNormalClosure, "server closing")

			return
		}

		// Reconnected: prove the express path survived the drop by pushing a hint.
		_ = wsjson.Write(context.Background(), conn, agents.ServerFrame{Type: agents.MsgTypeJobsAvailable})
		fake.serveReads(conn)
	})

	wsBackend := newWSBackend(t, fake, backend.WithLogger(slog.New(logs)))
	hints := wsBackend.Hints()

	worker, err := wsBackend.Register(ctx, nil)
	r.NoError(err)
	r.NotEmpty(worker.UID)

	// Drop the first connection from the server side.
	close(closeFirst)

	// The agent must reconnect proactively and then deliver the post-recovery hint.
	select {
	case got := <-hints:
		r.Equal("{}", got)
	case <-time.After(10 * time.Second):
		t.Fatal("hint from the reconnected connection never arrived — agent did not recover")
	}

	r.True(logs.has(slog.LevelWarn, "agent connection lost"), "the connection loss must be logged at Warn")
	r.Zero(fake.claimCount(), "reconnection must not issue any claim")
}

// TestWSBackendReconnectsOnHalfOpenPingFailure: on a half-open link (the server
// stops responding without closing) the agent detects the dead peer via its own
// keepalive ping and reconnects, instead of blocking on the OS TCP timeout.
func TestWSBackendReconnectsOnHalfOpenPingFailure(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	logs := newCapturingHandler()
	fake := newFakeAgentServer(t, "@acme/dc1")

	silent := make(chan struct{})
	t.Cleanup(func() { close(silent) })

	reconnected := make(chan struct{})

	var once sync.Once

	fake.handle(func(idx int, conn *websocket.Conn) {
		if idx == 0 {
			// Go silent: never read, so the agent's pings get no pong while the
			// TCP connection stays up — a half-open link.
			<-silent

			return
		}

		once.Do(func() { close(reconnected) })
		fake.serveReads(conn)
	})

	wsBackend := newWSBackend(t, fake,
		backend.WithLogger(slog.New(logs)),
		backend.WithPingInterval(200*time.Millisecond),
	)

	worker, err := wsBackend.Register(ctx, nil)
	r.NoError(err)
	r.NotEmpty(worker.UID)

	select {
	case <-reconnected:
	case <-time.After(10 * time.Second):
		t.Fatal("agent never detected the half-open link via ping and reconnected")
	}

	r.True(logs.has(slog.LevelWarn, "agent connection lost"), "a ping-detected loss must be logged at Warn")
}

// TestWSBackendRequestFailsFastOnDrop: a request in flight when the connection
// drops fails promptly (waking the waiter), not after the full 30s
// requestTimeout.
func TestWSBackendRequestFailsFastOnDrop(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	fake := newFakeAgentServer(t, "@acme/dc1")

	fake.handle(func(idx int, conn *websocket.Conn) {
		if idx == 0 {
			// Read the claim, then drop WITHOUT answering it.
			var frame agents.ClientFrame
			_ = wsjson.Read(context.Background(), conn, &frame)
			_ = conn.Close(websocket.StatusNormalClosure, "drop mid-request")

			return
		}

		fake.serveReads(conn)
	})

	wsBackend := newWSBackend(t, fake)

	worker, err := wsBackend.Register(ctx, nil)
	r.NoError(err)

	start := time.Now()
	_, claimErr := wsBackend.ClaimJobs(ctx, worker.UID, nil, 5, 5, time.Minute)
	elapsed := time.Since(start)

	r.Error(claimErr, "a claim whose connection drops mid-flight must fail")
	r.Less(elapsed, 5*time.Second, "the in-flight request must fail fast, not wait out the 30s requestTimeout")
}
