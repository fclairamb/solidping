package checkjs

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// runSocketScript executes a script exactly as a real check would.
func runSocketScript(t *testing.T, script string, timeout time.Duration) *checkerdef.Result {
	t.Helper()

	return runScriptWithContext(t.Context(), t, script, timeout)
}

// runScriptWithContext is runSocketScript with a caller-supplied context, which
// is how a tunnel dialer or a pinned IP version reaches the runtime.
func runScriptWithContext(
	ctx context.Context, t *testing.T, script string, timeout time.Duration,
) *checkerdef.Result {
	t.Helper()

	checker := &JSChecker{}

	result, err := checker.Execute(ctx, &JSConfig{Script: script, Timeout: timeout})
	require.NoError(t, err)
	require.NotNil(t, result)

	return result
}

// tcpFixture is a loopback TCP server that counts accepts. The count is what
// makes the negative tests real: asserting only on the error message would pass
// even if the connection had actually been made.
type tcpFixture struct {
	listener net.Listener
	accepts  atomic.Int32
	closed   chan struct{}
	closeOne sync.Once
}

// startTCPFixture serves handle on every accepted connection.
func startTCPFixture(t *testing.T, handle func(net.Conn)) *tcpFixture {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0") //nolint:noctx // loopback test fixture
	require.NoError(t, err)

	fixture := &tcpFixture{listener: listener, closed: make(chan struct{})}

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			fixture.accepts.Add(1)

			go func() {
				defer func() { _ = conn.Close() }()

				handle(conn)
			}()
		}
	}()

	return fixture
}

func (f *tcpFixture) addr() string {
	return f.listener.Addr().String()
}

// markClosed records that this fixture saw its peer go away, exactly once.
func (f *tcpFixture) markClosed() {
	f.closeOne.Do(func() { close(f.closed) })
}

// requireAccepts waits until the fixture has accepted exactly want connections
// and then confirms it accepts no more.
//
// The wait is not flake-padding: an accept is observed on the fixture's own
// goroutine and can lag a client dial the kernel already completed from the
// backlog. The settle-and-recheck afterwards is the assertion that matters for
// a REFUSED connect — "it never dialed" has to stay true a moment later, not
// just at the instant the script returned.
func requireAccepts(t *testing.T, fixture *tcpFixture, want int32) {
	t.Helper()

	require.Eventually(t, func() bool { return fixture.accepts.Load() == want },
		3*time.Second, 10*time.Millisecond,
		"expected exactly %d accepts, saw %d", want, fixture.accepts.Load())

	time.Sleep(200 * time.Millisecond)
	require.Equal(t, want, fixture.accepts.Load(), "the fixture accepted a connection it should not have")
}

// echoLines replies to each CRLF-terminated line the way a trivial RESP server
// would: PING -> +PONG, AUTH -> +OK, anything else -> -ERR.
func echoLines(conn net.Conn) {
	reader := bufio.NewReader(conn)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}

		switch {
		case strings.HasPrefix(line, "PING"):
			_, _ = conn.Write([]byte("+PONG\r\n"))
		case strings.HasPrefix(line, "AUTH"):
			_, _ = conn.Write([]byte("+OK\r\n"))
		default:
			_, _ = conn.Write([]byte("-ERR unknown\r\n"))
		}
	}
}

func TestSocketTCPConversationUntil(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := startTCPFixture(t, echoLines)

	result := runSocketScript(t, `
var c = tcp.connect("`+fixture.addr()+`");
if (!c.ok) return { status: "down", output: { error: c.error } };
c.write("AUTH hunter2\r\n");
var auth = c.read({ until: "\r\n" });
c.write("PING\r\n");
var pong = c.read({ until: "\r\n" });
c.close();
return {
  status: pong.data === "+PONG\r\n" ? "up" : "down",
  output: { auth: auth.data, pong: pong.data, remote: c.remoteAddr, family: c.ipVersion },
};
`, 10*time.Second)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	r.Equal("+OK\r\n", result.Output["auth"])
	r.Equal(fixture.addr(), result.Output["remote"])
	r.Equal("ipv4", result.Output["family"])
}

func TestSocketTCPReadModes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := startTCPFixture(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("0123456789abc"))

		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
	})

	result := runSocketScript(t, `
var c = tcp.connect("`+fixture.addr()+`");
var exact = c.read({ bytes: 4 });
var matched = c.read({ pattern: "[a-c]+" });
c.close();
return { status: "up", output: { exact: exact.data, exactBytes: exact.bytes, matched: matched.data } };
`, 10*time.Second)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	r.Equal("0123", result.Output["exact"])
	r.InDelta(4, result.Output["exactBytes"], 0.001)
	r.Contains(result.Output["matched"], "abc")
}

// A read with no criteria returns whatever the kernel hands over — the bare
// read the API documents.
func TestSocketTCPBareRead(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := startTCPFixture(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("hello"))

		time.Sleep(2 * time.Second)
	})

	result := runSocketScript(t, `
var c = tcp.connect("`+fixture.addr()+`");
var chunk = c.read();
c.close();
return { status: chunk.ok ? "up" : "down", output: { data: chunk.data, bytes: chunk.bytes } };
`, 10*time.Second)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	r.Equal("hello", result.Output["data"])
}

// A bare read that FILLS maxBytes is a truncated success, not a cap failure:
// the script asked for no match, so "no match in the first 16 bytes" would be a
// nonsense verdict on a call that returned exactly what it promised — "the next
// chunk the kernel hands over".
func TestSocketBareReadTruncatesAtMaxBytes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := startTCPFixture(t, func(conn net.Conn) {
		_, _ = conn.Write(bytes.Repeat([]byte("x"), 4096))

		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
	})

	result := runSocketScript(t, `
var c = tcp.connect("`+fixture.addr()+`");
var chunk = c.read({ maxBytes: 16, timeout: "5s" });
c.close();
return {
  status: "up",
  output: { ok: chunk.ok, bytes: chunk.bytes, data: chunk.data, error: chunk.error, eof: chunk.eof },
};
`, 15*time.Second)

	r.Equal(true, result.Output["ok"], "output: %#v", result.Output)
	r.InDelta(16, result.Output["bytes"], 0.001)
	r.Equal(strings.Repeat("x", 16), result.Output["data"])
	r.Equal(false, result.Output["eof"])
	r.Nil(result.Output["error"])
}

// startUDPBigReplier answers every datagram with `size` bytes, so a receive
// with a smaller maxBytes has something real to truncate.
func startUDPBigReplier(t *testing.T, size int) net.PacketConn {
	t.Helper()

	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0") //nolint:noctx // loopback test fixture
	require.NoError(t, err)

	t.Cleanup(func() { _ = packetConn.Close() })

	reply := bytes.Repeat([]byte{0xAB}, size)

	go func() {
		buf := make([]byte, 2048)

		for {
			_, addr, readErr := packetConn.ReadFrom(buf)
			if readErr != nil {
				return
			}

			_, _ = packetConn.WriteTo(reply, addr)
		}
	}()

	return packetConn
}

// §1's promise for UDP: "a datagram longer than maxBytes is truncated and
// `bytes` reports what was kept". Truncated, not refused.
func TestSocketUDPReceiveTruncatesLongDatagram(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	replier := startUDPBigReplier(t, 1024)

	result := runSocketScript(t, `
var u = udp.open("`+replier.LocalAddr().String()+`");
u.send("01");
var reply = u.receive({ timeout: "3s", encoding: "hex", maxBytes: 512 });
u.close();
return { status: "up", output: { ok: reply.ok, bytes: reply.bytes, hexLength: reply.data.length, error: reply.error } };
`, 15*time.Second)

	r.Equal(true, result.Output["ok"], "output: %#v", result.Output)
	r.InDelta(512, result.Output["bytes"], 0.001)
	r.InDelta(1024, result.Output["hexLength"], 0.001)
	r.Nil(result.Output["error"])
}

// makeTestCertificate mints a self-signed key+cert pair for the loopback TLS
// listener below.
func makeTestCertificate(t *testing.T) tls.Certificate {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// TestSocketTCPTLS is the negative-with-a-positive-control the spec asks for:
// the SAME self-signed listener is reachable with tlsVerify:false and refused
// with the default tlsVerify:true, so neither outcome can be produced by the
// TLS option simply being ignored.
func TestSocketTCPTLS(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{makeTestCertificate(t)},
		MinVersion:   tls.VersionTLS12,
	})
	r.NoError(err)

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			go func() {
				defer func() { _ = conn.Close() }()

				echoLines(conn)
			}()
		}
	}()

	addr := listener.Addr().String()

	insecure := runSocketScript(t, `
var c = tcp.connect("`+addr+`", { tls: true, tlsVerify: false });
if (!c.ok) return { status: "down", output: { error: c.error } };
c.write("PING\r\n");
var pong = c.read({ until: "\r\n" });
c.close();
return {
  status: pong.data === "+PONG\r\n" ? "up" : "down",
  output: { version: c.tls.version, cipher: c.tls.cipherSuite },
};
`, 10*time.Second)

	r.Equal("up", insecure.Status.String(), "output: %#v", insecure.Output)
	r.Contains(insecure.Output["version"], "TLS 1.")
	r.NotEmpty(insecure.Output["cipher"])

	verified := runSocketScript(t, `
var c = tcp.connect("`+addr+`", { tls: true });
return { status: c.ok ? "up" : "down", output: { error: c.error } };
`, 10*time.Second)

	r.Equal("down", verified.Status.String(), "output: %#v", verified.Output)
	r.Contains(verified.Output["error"], "certificate")
}

// startUDPEcho is a loopback datagram echo server.
func startUDPEcho(t *testing.T) net.PacketConn {
	t.Helper()

	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0") //nolint:noctx // loopback test fixture
	require.NoError(t, err)

	t.Cleanup(func() { _ = packetConn.Close() })

	go func() {
		buf := make([]byte, 2048)

		for {
			n, addr, readErr := packetConn.ReadFrom(buf)
			if readErr != nil {
				return
			}

			_, _ = packetConn.WriteTo(buf[:n], addr)
		}
	}()

	return packetConn
}

func TestSocketUDPEcho(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	echo := startUDPEcho(t)

	result := runSocketScript(t, `
var u = udp.open("`+echo.LocalAddr().String()+`");
if (!u.ok) return { status: "down", output: { error: u.error } };
u.send("ffffffff54536f75726365", { encoding: "hex" });
var reply = u.receive({ timeout: "2s", encoding: "hex" });
u.close();
return { status: reply.ok ? "up" : "down", output: { data: reply.data, bytes: reply.bytes, remote: u.remoteAddr } };
`, 10*time.Second)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	r.Equal("ffffffff54536f75726365", result.Output["data"])
	r.Equal(echo.LocalAddr().String(), result.Output["remote"])
}

// A datagram is the unit: read criteria are refused rather than silently
// ignored, because "accumulate until" has no meaning over UDP.
func TestSocketUDPRefusesReadCriteria(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	echo := startUDPEcho(t)

	result := runSocketScript(t, `
var u = udp.open("`+echo.LocalAddr().String()+`");
var reply = u.receive({ until: "\r\n" });
return { status: "up", output: { ok: reply.ok, error: reply.error } };
`, 10*time.Second)

	r.Equal(false, result.Output["ok"])
	r.Contains(result.Output["error"], "one datagram")
}

// TestSocketConnectRefused: a port nothing listens on classifies as refused,
// with the same class string every other checker reports.
func TestSocketConnectRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0") //nolint:noctx // loopback test fixture
	r.NoError(err)

	addr := listener.Addr().String()
	r.NoError(listener.Close())

	result := runSocketScript(t, `
var c = tcp.connect("`+addr+`", { timeout: "2s" });
return { status: c.ok ? "up" : "down", output: { error: c.error, class: c.class } };
`, 10*time.Second)

	r.Equal("down", result.Status.String(), "output: %#v", result.Output)
	r.Equal(checkerdef.NetFailureConnectionRefused, result.Output["class"])
	r.Contains(result.Output["error"], "connection failed")
}

func TestSocketConnectUnresolvable(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := runSocketScript(t, `
var c = tcp.connect("nothing-here.invalid:6379", { timeout: "3s" });
return { status: c.ok ? "up" : "down", output: { error: c.error, hasClass: c.class !== undefined } };
`, 10*time.Second)

	r.Equal("down", result.Status.String(), "output: %#v", result.Output)
	r.Contains(result.Output["error"], "resolve")
	// A name that does not resolve has no address to trace to, so it carries
	// no reachability class — same rule as ClassifyDialError's DNS branch.
	r.Equal(false, result.Output["hasClass"])
}

func TestSocketReadReportsPeerEOF(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := startTCPFixture(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("bye"))
	})

	result := runSocketScript(t, `
var c = tcp.connect("`+fixture.addr()+`");
var first = c.read({ until: "NEVER" });
c.close();
return { status: "up", output: { ok: first.ok, eof: first.eof, data: first.data, error: first.error } };
`, 10*time.Second)

	r.Equal(false, result.Output["ok"])
	r.Equal(true, result.Output["eof"])
	r.Equal("bye", result.Output["data"])
	r.Contains(result.Output["error"], "closed by peer")
}

// A per-call timeout is a VALUE, and it keeps the partial data — that partial
// is often the whole diagnostic ("it sent the banner and then stalled").
func TestSocketReadPerCallTimeoutKeepsPartialData(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := startTCPFixture(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte("partial"))

		time.Sleep(3 * time.Second)
	})

	result := runSocketScript(t, `
var c = tcp.connect("`+fixture.addr()+`");
var stalled = c.read({ until: "\r\n", timeout: "300ms" });
c.close();
return { status: "up", output: { ok: stalled.ok, timedOut: stalled.timedOut, data: stalled.data } };
`, 10*time.Second)

	r.Equal(false, result.Output["ok"])
	r.Equal(true, result.Output["timedOut"])
	r.Equal("partial", result.Output["data"])
}

// TestSocketExecutionDeadlineIsATimeout: a script still blocked in read() when
// the CHECK's own budget runs out reports `timeout`, not a script error — and
// the connection is disposed, which the fixture observes as EOF.
func TestSocketExecutionDeadlineIsATimeout(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var fixture *tcpFixture

	fixture = startTCPFixture(t, func(conn net.Conn) {
		buf := make([]byte, 1)
		// Returns when our side goes away — which is what proves the handle
		// was disposed rather than left dangling past the deadline.
		_, _ = conn.Read(buf)

		fixture.markClosed()
	})

	result := runSocketScript(t, `
var c = tcp.connect("`+fixture.addr()+`");
c.read({ until: "NEVER ARRIVES" });
return { status: "up" };
`, time.Second)

	r.Equal("timeout", result.Status.String(), "output: %#v", result.Output)

	select {
	case <-fixture.closed:
	case <-time.After(5 * time.Second):
		r.Fail("the fixture never saw the connection close")
	}
}

// The positive control for the test above: the SAME blocked read, with a short
// per-call timeout, is a value — the script keeps running and the connection
// stays usable right up to close().
func TestSocketPerCallTimeoutKeepsTheConnectionAlive(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := startTCPFixture(t, echoLines)

	result := runSocketScript(t, `
var c = tcp.connect("`+fixture.addr()+`");
var stalled = c.read({ until: "NEVER", timeout: "200ms" });
c.write("PING\r\n");
var pong = c.read({ until: "\r\n", timeout: "2s" });
c.close();
return {
  status: pong.data === "+PONG\r\n" ? "up" : "down",
  output: { timedOut: stalled.timedOut },
};
`, 10*time.Second)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	r.Equal(true, result.Output["timedOut"])
}

// TestSocketConnectionBudget: the sixth connect is refused, and the fixture's
// accept count proves the refusal happened BEFORE any dial.
func TestSocketConnectionBudget(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := startTCPFixture(t, func(conn net.Conn) {
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
	})

	result := runSocketScript(t, `
var last = null;
var opened = 0;
for (var i = 0; i < 6; i++) {
  var c = tcp.connect("`+fixture.addr()+`");
  if (c.ok) { opened++; } else { last = c.error; }
}
return { status: "up", output: { opened: opened, error: last } };
`, 15*time.Second)

	r.InDelta(5, result.Output["opened"], 0.001)
	r.Contains(result.Output["error"], "connection limit of 5 exceeded")
	requireAccepts(t, fixture, 5)
}

// A connect spends one unit of the SAME 20-call budget http.* draws from, so a
// script that spent 19 calls on HTTP has exactly one connection left.
func TestSocketConnectSharesTheSubCheckBudget(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(backend.Close)

	fixture := startTCPFixture(t, func(conn net.Conn) {
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
	})

	result := runSocketScript(t, `
for (var i = 0; i < 19; i++) { http.get("`+backend.URL+`"); }
var first = tcp.connect("`+fixture.addr()+`");
var second = tcp.connect("`+fixture.addr()+`");
return {
  status: "up",
  output: { firstOk: first.ok, secondOk: second.ok, secondError: second.error },
};
`, 20*time.Second)

	r.Equal(true, result.Output["firstOk"], "output: %#v", result.Output)
	r.Equal(false, result.Output["secondOk"])
	r.Contains(result.Output["secondError"], "sub-check limit of 20 exceeded")
	requireAccepts(t, fixture, 1)
}

// TestSocketActionBudget: the 201st read/write is refused, and the counter is
// its OWN — the 200 actions above did not touch the 20-call budget, which the
// http.get after them proves by still working.
func TestSocketActionBudget(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("still-here"))
	}))
	t.Cleanup(backend.Close)

	fixture := startTCPFixture(t, func(conn net.Conn) {
		drain := make([]byte, 4096)

		for {
			if _, err := conn.Read(drain); err != nil {
				return
			}
		}
	})

	result := runSocketScript(t, `
var c = tcp.connect("`+fixture.addr()+`");
var refused = null;
for (var i = 0; i < 201; i++) {
  var w = c.write("x");
  if (!w.ok) { refused = w.error; }
}
c.close();
var after = http.get("`+backend.URL+`");
return { status: "up", output: { refused: refused, body: after.body } };
`, 20*time.Second)

	r.Contains(result.Output["refused"], "socket action limit of 200 exceeded")
	r.Equal("still-here", result.Output["body"])
}

// TestSocketReadCapAndSharedPayloadPool proves both halves of the byte
// accounting in one run: a read that never matches stops at its own maxBytes
// with the cap message, and the bytes it DID consume come out of the same 1 MiB
// pool a later http.get reads its body from.
func TestSocketReadCapAndSharedPayloadPool(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("a-body-that-should-not-fit"))
	}))
	t.Cleanup(backend.Close)

	// Streams 'x' forever and never sends the delimiter the script waits for.
	fixture := startTCPFixture(t, func(conn net.Conn) {
		block := make([]byte, 32*1024)
		for i := range block {
			block[i] = 'x'
		}

		for {
			if _, err := conn.Write(block); err != nil {
				return
			}
		}
	})

	result := runSocketScript(t, `
var c = tcp.connect("`+fixture.addr()+`");
var small = c.read({ until: "\r\n", maxBytes: 16, timeout: "5s" });
var bulk = c.read({ until: "\r\n", maxBytes: 1048576, timeout: "10s" });
c.close();
var after = http.get("`+backend.URL+`");
return {
  status: "up",
  output: {
    smallOk: small.ok, smallBytes: small.bytes, smallError: small.error,
    bulkBytes: bulk.bytes, bodyLength: after.body.length,
  },
};
`, 25*time.Second)

	r.Equal(false, result.Output["smallOk"], "output: %#v", result.Output)
	r.InDelta(16, result.Output["smallBytes"], 0.001)
	r.Contains(result.Output["smallError"], "no match in the first 16 bytes of the reply")

	// The bulk read drained what was left of the shared pool, so the HTTP body
	// after it comes back empty rather than with its own fresh 1 MiB.
	r.InDelta(0, result.Output["bodyLength"], 0.001)
}

// TestSocketTypeGate: an operator who turned a transport off does not get it
// back through a script. Each half has its positive control, and each fixture's
// accept count proves nothing was dialed.
//
//nolint:paralleltest // mutates the package-level TypeEnabled global
func TestSocketTypeGate(t *testing.T) {
	r := require.New(t)

	fixture := startTCPFixture(t, echoLines)
	echo := startUDPEcho(t)
	wsServer := startWSFixture(t, echoWSFrames)

	installGate(t, checkerdef.CheckTypeTCP, checkerdef.CheckTypeUDP, checkerdef.CheckTypeWebSocket)

	for _, tc := range []struct {
		name   string
		script string
		want   string
	}{
		{"tcp", `tcp.connect("` + fixture.addr() + `");`, `check type "tcp" is disabled on this server`},
		{"udp", `udp.open("` + echo.LocalAddr().String() + `");`, `check type "udp" is disabled on this server`},
		{"websocket", `websocket.connect("` + wsURL(wsServer) + `");`, `check type "websocket" is disabled on this server`},
	} {
		result := runSocketScript(t, tc.script+"\nreturn { status: \"up\" };", 10*time.Second)

		r.Equal("error", result.Status.String(), "%s: output %#v", tc.name, result.Output)
		r.Contains(result.Output["error"], tc.want, tc.name)
	}

	requireAccepts(t, fixture, 0)

	// Positive control: with the gate open the very same scripts connect.
	clearGate(t)

	open := runSocketScript(t, `
var c = tcp.connect("`+fixture.addr()+`");
var u = udp.open("`+echo.LocalAddr().String()+`");
var w = websocket.connect("`+wsURL(wsServer)+`");
return { status: "up", output: { tcp: c.ok, udp: u.ok, ws: w.ok } };
`, 10*time.Second)

	r.Equal(true, open.Output["tcp"], "output: %#v", open.Output)
	r.Equal(true, open.Output["udp"])
	r.Equal(true, open.Output["ws"])
	requireAccepts(t, fixture, 1)
}

// recordingDialer stands in for the SSH bastion: it records the address it was
// handed and connects to a local fixture instead.
type recordingDialer struct {
	target    string
	requested []string
}

func (d *recordingDialer) dial(_ context.Context, network, addr string) (net.Conn, error) {
	d.requested = append(d.requested, network+"|"+addr)

	return net.Dial(network, d.target) //nolint:noctx // test fixture, fixed loopback address
}

// tunnelHost is a name RFC 2606 guarantees never resolves. Only the fake
// bastion knows what it means — so a handle that connects to it proves no local
// resolution happened.
const tunnelHost = "private.invalid:6379"

// TestSocketTunnelledTCP is written and tested now even though the `js` type
// cannot be tunneled until spec 2026-09-15-07 lands: when that flag flips the
// sockets are already correct.
func TestSocketTunnelledTCP(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := startTCPFixture(t, echoLines)
	dialer := &recordingDialer{target: fixture.addr()}

	ctx := checkerdef.WithTunnelDialer(t.Context(), checkerdef.DialerFunc(dialer.dial))

	result := runScriptWithContext(ctx, t, `
var c = tcp.connect("`+tunnelHost+`");
if (!c.ok) return { status: "down", output: { error: c.error } };
c.write("PING\r\n");
var pong = c.read({ until: "\r\n", timeout: "2s" });
c.close();
return {
  status: pong.data === "+PONG\r\n" ? "up" : "down",
  output: { tunneled: c.tunneled, hasRemote: c.remoteAddr !== undefined, hasFamily: c.ipVersion !== undefined },
};
`, 10*time.Second)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
	r.Equal(true, result.Output["tunneled"])
	r.Equal(false, result.Output["hasRemote"], "a tunneled handle must not invent a remote address")
	r.Equal(false, result.Output["hasFamily"])
	r.Equal([]string{"tcp|" + tunnelHost}, dialer.requested,
		"the bastion must be handed the raw host:port, verbatim")
}

// An SSH direct-tcpip forward carries TCP only, so udp.open under a tunnel is
// refused — and, crucially, never falls back to dialing from this worker.
func TestSocketTunnelledUDPIsRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	echo := startUDPEcho(t)
	dialer := &recordingDialer{target: echo.LocalAddr().String()}

	ctx := checkerdef.WithTunnelDialer(t.Context(), checkerdef.DialerFunc(dialer.dial))

	result := runScriptWithContext(ctx, t, `
var u = udp.open("`+tunnelHost+`");
return { status: "up", output: { ok: u.ok, error: u.error } };
`, 10*time.Second)

	r.Equal(false, result.Output["ok"])
	r.Contains(result.Output["error"], "UDP cannot be tunneled")
	r.Empty(dialer.requested, "a tunneled udp.open must never dial")
}

// The address family is chosen by the bastion, so pinning it here is refused
// the same way the API refuses the pair on a check config.
func TestSocketTunnelledIPVersionIsRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := startTCPFixture(t, echoLines)
	dialer := &recordingDialer{target: fixture.addr()}

	ctx := checkerdef.WithTunnelDialer(t.Context(), checkerdef.DialerFunc(dialer.dial))

	result := runScriptWithContext(ctx, t, `
var c = tcp.connect("`+tunnelHost+`", { ipVersion: "ipv4" });
return { status: "up", output: { ok: c.ok, error: c.error } };
`, 10*time.Second)

	r.Equal(false, result.Output["ok"])
	r.Contains(result.Output["error"], "cannot be pinned here")
	r.Empty(dialer.requested)
}

// TestSocketLifecycle: close() twice is fine, and every method after a close
// answers "not connected" rather than throwing or dereferencing undefined.
func TestSocketLifecycle(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := startTCPFixture(t, echoLines)

	result := runSocketScript(t, `
var c = tcp.connect("`+fixture.addr()+`");
var first = c.close();
var second = c.close();
var write = c.write("PING\r\n");
var read = c.read();
return {
  status: "up",
  output: {
    first: first.ok, second: second.ok,
    writeOk: write.ok, writeError: write.error,
    readOk: read.ok, readError: read.error,
  },
};
`, 10*time.Second)

	r.Equal(true, result.Output["first"])
	r.Equal(true, result.Output["second"])
	r.Equal(false, result.Output["writeOk"])
	r.Equal("not connected", result.Output["writeError"])
	r.Equal(false, result.Output["readOk"])
	r.Equal("not connected", result.Output["readError"])
}

// A handle whose connect FAILED answers the same way, so a script that forgot
// to check `c.ok` fails loudly on the next line.
func TestSocketMethodsOnAFailedHandle(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := runSocketScript(t, `
var c = tcp.connect("nothing-here.invalid:6379", { timeout: "3s" });
var write = c.write("PING\r\n");
return { status: "up", output: { connectOk: c.ok, writeOk: write.ok, writeError: write.error } };
`, 10*time.Second)

	r.Equal(false, result.Output["connectOk"])
	r.Equal(false, result.Output["writeOk"])
	r.Equal("not connected", result.Output["writeError"])
}

// A script that returns without closing still leaks nothing: Execute's defer
// disposes the handle, which the fixture sees as EOF.
func TestSocketUnclosedHandleIsDisposed(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	closed := make(chan struct{})

	var once sync.Once

	fixture := startTCPFixture(t, func(conn net.Conn) {
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)

		once.Do(func() { close(closed) })
	})

	result := runSocketScript(t, `
tcp.connect("`+fixture.addr()+`");
return { status: "up" };
`, 10*time.Second)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		r.Fail("the unclosed handle was never disposed")
	}
}

// Invalid option values are VALUES, not throws — the same split http.* makes.
func TestSocketInvalidOptionsReturn(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fixture := startTCPFixture(t, echoLines)

	result := runSocketScript(t, `
var badTimeout = tcp.connect("`+fixture.addr()+`", { timeout: "not-a-duration" });
var c = tcp.connect("`+fixture.addr()+`");
var badEncoding = c.write("hi", { encoding: "rot13" });
var badPattern = c.read({ pattern: "([" });
var conflict = c.read({ bytes: 4, until: "\r\n" });
c.close();
return {
  status: "up",
  output: {
    timeoutError: badTimeout.error,
    encodingError: badEncoding.error,
    patternError: badPattern.error,
    conflictError: conflict.error,
  },
};
`, 10*time.Second)

	r.Contains(result.Output["timeoutError"], "invalid timeout")
	r.Contains(result.Output["encodingError"], "unknown encoding")
	r.Contains(result.Output["patternError"], "invalid pattern")
	r.Contains(result.Output["conflictError"], `one of "bytes", "until" or "pattern"`)
}

// expiredNotYetCancelledContext is the skew window the execution-deadline check
// has to survive: the deadline has passed, but the context's own timer has not
// marked it done yet. A blocked socket read stops on ITS deadline — a different
// clock — so it can land in exactly this state.
type expiredNotYetCancelledContext struct{}

func (expiredNotYetCancelledContext) Deadline() (time.Time, bool) {
	return time.Now().Add(-time.Millisecond), true
}

func (expiredNotYetCancelledContext) Done() <-chan struct{} { return nil }

func (expiredNotYetCancelledContext) Err() error { return nil }

func (expiredNotYetCancelledContext) Value(any) any { return nil }

// TestExecutionDeadlinePanicsOnClockSkew: past the execution deadline is the
// check's budget running out, even when ctx.Err() has not caught up. Reporting
// it as a per-call timeout value would let the script keep running and return
// `up` — which is the CI flake this guards.
func TestExecutionDeadlinePanicsOnClockSkew(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	runtime := &jsRuntime{execCtx: expiredNotYetCancelledContext{}, vm: goja.New()}

	r.Panics(runtime.panicOnExecutionDeadline)
}

// The positive control: inside the budget, the same call is a no-op — otherwise
// the test above would pass with a check that always panics.
func TestExecutionDeadlineDoesNotPanicInsideTheBudget(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	runtime := &jsRuntime{execCtx: ctx, vm: goja.New()}

	r.NotPanics(runtime.panicOnExecutionDeadline)
}
