package checkjs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// startWSFixture serves handle on every accepted WebSocket connection.
func startWSFixture(t *testing.T, handle func(*websocket.Conn, *http.Request)) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}

		defer func() { _ = conn.CloseNow() }()

		handle(conn, req)
	}))

	t.Cleanup(server.Close)

	return server
}

// wsURL turns an httptest URL into the ws:// one a script connects to.
func wsURL(server *httptest.Server) string {
	return strings.Replace(server.URL, "http://", "ws://", 1)
}

// echoWSFrames bounces every frame back with its type preserved.
func echoWSFrames(conn *websocket.Conn, req *http.Request) {
	for {
		messageType, payload, err := conn.Read(req.Context())
		if err != nil {
			return
		}

		if err := conn.Write(req.Context(), messageType, payload); err != nil {
			return
		}
	}
}

func TestWebSocketTextAndBinaryFrames(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := startWSFixture(t, echoWSFrames)

	result := runSocketScript(t, `
var ws = websocket.connect("`+wsURL(server)+`");
if (!ws.ok) return { status: "down", output: { error: ws.error, statusCode: ws.statusCode } };
ws.send("hello");
var text = ws.receive({ timeout: "5s" });
ws.send("cafe", { type: "binary", encoding: "hex" });
var binary = ws.receive({ timeout: "5s", encoding: "hex" });
ws.close({ code: 1000 });
return {
  status: "up",
  output: {
    statusCode: ws.statusCode,
    textType: text.type, textData: text.data,
    binaryType: binary.type, binaryData: binary.data,
  },
};
`, 15*time.Second)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	r.InDelta(http.StatusSwitchingProtocols, result.Output["statusCode"], 0.001)
	r.Equal("text", result.Output["textType"])
	r.Equal("hello", result.Output["textData"])
	r.Equal("binary", result.Output["binaryType"])
	r.Equal("cafe", result.Output["binaryData"])
}

// A subscribe-then-wait feed: frames before the one that matters are skipped by
// the SCRIPT, which is the whole reason a handle beats a one-shot check.
func TestWebSocketSubscribeSkipsUnrelatedFrames(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := startWSFixture(t, func(conn *websocket.Conn, req *http.Request) {
		ctx := req.Context()

		if _, _, err := conn.Read(ctx); err != nil {
			return
		}

		for _, frame := range []string{`{"type":"noise"}`, `{"type":"noise"}`, `{"type":"heartbeat","seq":7}`} {
			if err := conn.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
				return
			}
		}

		<-ctx.Done()
	})

	result := runSocketScript(t, `
var ws = websocket.connect("`+wsURL(server)+`");
ws.send(JSON.stringify({ op: "subscribe", channel: "heartbeat" }));
var seen = 0;
for (var i = 0; i < 5; i++) {
  var frame = ws.receive({ timeout: "3s" });
  if (!frame.ok) break;
  seen++;
  var event = JSON.parse(frame.data);
  if (event.type === "heartbeat") {
    ws.close();
    return { status: "up", metrics: { seq: event.seq }, output: { skipped: seen - 1 } };
  }
}
ws.close();
return { status: "down", output: { error: "no heartbeat frame" } };
`, 15*time.Second)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	r.InDelta(7, result.Metrics["seq"], 0.001)
	r.InDelta(2, result.Output["skipped"], 0.001)
}

// A handshake the server REJECTS keeps its HTTP status: a 401 on the upgrade is
// a completely different incident from a connection that reached no server.
func TestWebSocketHandshakeRejection(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	result := runSocketScript(t, `
var ws = websocket.connect("`+wsURL(server)+`", { headers: { Authorization: "Bearer wrong" } });
var frame = ws.receive({ timeout: "1s" });
return {
  status: ws.ok ? "up" : "down",
  output: { error: ws.error, statusCode: ws.statusCode, frameError: frame.error },
};
`, 15*time.Second)

	r.Equal("down", result.Status.String(), "output: %#v", result.Output)
	r.InDelta(http.StatusUnauthorized, result.Output["statusCode"], 0.001)
	r.NotEmpty(result.Output["error"])
	r.Equal("not connected", result.Output["frameError"])
}

// The headers option must actually reach the server — the positive control for
// the rejection test above.
func TestWebSocketSendsHeaders(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := startWSFixture(t, func(conn *websocket.Conn, req *http.Request) {
		_ = conn.Write(req.Context(), websocket.MessageText, []byte(req.Header.Get("Authorization")))

		<-req.Context().Done()
	})

	result := runSocketScript(t, `
var ws = websocket.connect("`+wsURL(server)+`", { headers: { Authorization: "Bearer tok-xyz" } });
var seen = ws.receive({ timeout: "5s" });
ws.close();
return { status: "up", output: { seen: seen.data } };
`, 15*time.Second)

	r.Equal("Bearer tok-xyz", result.Output["seen"])
}

// A per-call receive timeout is a VALUE the script inspects, not a throw — so a
// feed that goes quiet is the SCRIPT's verdict to make.
//
// It is also where WebSocket differs from a raw socket, and the difference is
// the library's, not ours: coder/websocket cannot resume a half-read frame, so
// a Read whose context expires CLOSES the connection. A TCP handle survives its
// per-call timeout (see TestSocketPerCallTimeoutKeepsTheConnectionAlive); a
// WebSocket handle does not, and the next call says so rather than hanging.
func TestWebSocketReceiveTimeoutIsAValue(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Accepts, then says nothing at all.
	server := startWSFixture(t, func(_ *websocket.Conn, req *http.Request) {
		<-req.Context().Done()
	})

	result := runSocketScript(t, `
var ws = websocket.connect("`+wsURL(server)+`");
var quiet = ws.receive({ timeout: "150ms" });
var after = ws.receive({ timeout: "1s" });
ws.close();
return {
  status: "up",
  output: {
    quietOk: quiet.ok, timedOut: quiet.timedOut, quietError: quiet.error,
    afterOk: after.ok, afterError: after.error,
  },
};
`, 15*time.Second)

	r.Equal(false, result.Output["quietOk"], "output: %#v", result.Output)
	r.Equal(true, result.Output["timedOut"])
	r.Contains(result.Output["quietError"], "timeout")
	r.Equal(false, result.Output["afterOk"], "a timed-out WebSocket read closes the connection")
	r.Contains(result.Output["afterError"], "closed")
}

// A server that closes gives eof, not a timeout.
func TestWebSocketPeerCloseIsEOF(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := startWSFixture(t, func(conn *websocket.Conn, _ *http.Request) {
		_ = conn.Close(websocket.StatusNormalClosure, "done")
	})

	result := runSocketScript(t, `
var ws = websocket.connect("`+wsURL(server)+`");
var frame = ws.receive({ timeout: "5s" });
ws.close();
return { status: "up", output: { ok: frame.ok, eof: frame.eof, timedOut: frame.timedOut, error: frame.error } };
`, 15*time.Second)

	r.Equal(false, result.Output["ok"], "output: %#v", result.Output)
	r.Equal(true, result.Output["eof"])
	r.Equal(false, result.Output["timedOut"])
}

// A frame over the per-call maxBytes fails rather than silently truncating, and
// the handle reflects the library's close on the next call.
func TestWebSocketFrameOverReadLimit(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := startWSFixture(t, func(conn *websocket.Conn, req *http.Request) {
		_ = conn.Write(req.Context(), websocket.MessageText, []byte(strings.Repeat("x", 4096)))

		<-req.Context().Done()
	})

	result := runSocketScript(t, `
var ws = websocket.connect("`+wsURL(server)+`");
var frame = ws.receive({ timeout: "5s", maxBytes: 64 });
ws.close();
return { status: "up", output: { ok: frame.ok, error: frame.error } };
`, 15*time.Second)

	r.Equal(false, result.Output["ok"], "output: %#v", result.Output)
	r.NotEmpty(result.Output["error"])
}

// websocket.connect spends the same connection budget tcp.connect does — the
// cap is on connections per execution, whatever their transport.
func TestWebSocketSharesTheConnectionBudget(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := startTCPFixture(t, echoLines)
	server := startWSFixture(t, echoWSFrames)

	result := runSocketScript(t, `
var opened = 0;
for (var i = 0; i < 3; i++) {
  if (tcp.connect("`+fixture.addr()+`").ok) { opened++; }
}
var third = websocket.connect("`+wsURL(server)+`");
var fourth = websocket.connect("`+wsURL(server)+`");
var refused = websocket.connect("`+wsURL(server)+`");
return {
  status: "up",
  output: { opened: opened, thirdOk: third.ok, fourthOk: fourth.ok, refusedOk: refused.ok, error: refused.error },
};
`, 20*time.Second)

	r.InDelta(3, result.Output["opened"], 0.001)
	r.Equal(true, result.Output["thirdOk"], "output: %#v", result.Output)
	r.Equal(true, result.Output["fourthOk"])
	r.Equal(false, result.Output["refusedOk"])
	r.Contains(result.Output["error"], "connection limit of 5 exceeded")
}

// A script that returns without closing still leaves nothing behind.
func TestWebSocketUnclosedHandleIsDisposed(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	gone := make(chan struct{})

	var once sync.Once

	server := startWSFixture(t, func(conn *websocket.Conn, req *http.Request) {
		_, _, _ = conn.Read(req.Context())

		once.Do(func() { close(gone) })
	})

	result := runSocketScript(t, `
websocket.connect("`+wsURL(server)+`");
return { status: "up" };
`, 15*time.Second)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)

	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		r.Fail("the unclosed WebSocket handle was never disposed")
	}
}

// TestWebSocketTunnelled: the handshake's underlying dial goes through the
// bastion, with the raw host:port handed over verbatim.
func TestWebSocketTunnelled(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	server := startWSFixture(t, echoWSFrames)

	backend := strings.TrimPrefix(server.URL, "http://")
	dialer := &recordingDialer{target: backend}

	ctx := checkerdef.WithTunnelDialer(t.Context(), checkerdef.DialerFunc(dialer.dial))

	result := runScriptWithContext(ctx, t, `
var ws = websocket.connect("ws://private.invalid/feed");
if (!ws.ok) return { status: "down", output: { error: ws.error } };
ws.send("ping");
var frame = ws.receive({ timeout: "5s" });
ws.close();
return { status: frame.data === "ping" ? "up" : "down", output: { tunneled: ws.tunneled } };
`, 15*time.Second)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	r.Equal(true, result.Output["tunneled"])
	r.Len(dialer.requested, 1)
	r.Contains(dialer.requested[0], "private.invalid:80")
}
