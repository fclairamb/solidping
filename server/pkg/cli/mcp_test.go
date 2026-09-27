package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/apihelper"
	"github.com/fclairamb/solidping/server/pkg/cli/config"
)

const (
	testMCPSessionID = "11111111-2222-4333-8444-555555555555"
	testMCPTimeout   = 10 * time.Second
)

// fakeMCPRequest is what the fake server saw for one HTTP request.
type fakeMCPRequest struct {
	httpMethod    string
	rpcMethod     string
	authorization string
	sessionID     string
}

// fakeMCPServer is an in-process stand-in for /api/v1/mcp. It accepts one
// bearer token, mints a session on initialize, answers tools/list as an SSE
// stream and tools/call as pretty-printed JSON, so the bridge's framing is
// exercised on both content types.
type fakeMCPServer struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	valid  string
	seen   []fakeMCPRequest
	extras map[string]http.HandlerFunc
	// noScope is a token the server knows but that lacks the mcp scope: it
	// gets the real handler's 403 JSON-RPC error, which carries no id.
	noScope string
	// sseInitialize answers initialize as an event stream.
	sseInitialize bool
}

// configure changes the fake's behavior under its lock, before any request.
func (f *fakeMCPServer) configure(change func(*fakeMCPServer)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	change(f)
}

func newFakeMCPServer(t *testing.T, validToken string) *fakeMCPServer {
	t.Helper()

	fake := &fakeMCPServer{t: t, valid: validToken, extras: map[string]http.HandlerFunc{}}
	fake.srv = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.srv.Close)

	return fake
}

func (f *fakeMCPServer) requests() []fakeMCPRequest {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]fakeMCPRequest(nil), f.seen...)
}

func (f *fakeMCPServer) setExtra(path string, handler http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.extras[path] = handler
}

func (f *fakeMCPServer) serve(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	handler, ok := f.extras[req.URL.Path]
	f.mu.Unlock()

	if ok {
		handler(w, req)

		return
	}

	if req.URL.Path != mcpAPIPath {
		w.WriteHeader(http.StatusNotFound)

		return
	}

	var env struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}

	body, _ := io.ReadAll(req.Body)
	_ = json.Unmarshal(body, &env)

	f.mu.Lock()
	f.seen = append(f.seen, fakeMCPRequest{
		httpMethod:    req.Method,
		rpcMethod:     env.Method,
		authorization: req.Header.Get("Authorization"),
		sessionID:     req.Header.Get(mcpHeaderSessionID),
	})
	noScope, sseInitialize := f.noScope, f.sseInitialize
	f.mu.Unlock()

	if noScope != "" && req.Header.Get("Authorization") == "Bearer "+noScope {
		// Same shape as mcp.Handler.Handle: errorResponse(nil, ...) omits the id.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32002,"message":"Token lacks mcp or mcp:read scope"}}`))

		return
	}

	if req.Header.Get("Authorization") != "Bearer "+f.valid {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="x"`)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"title":"Invalid or expired token","code":"INVALID_TOKEN"}`))

		return
	}

	if req.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)

		return
	}

	switch env.Method {
	case "initialize":
		w.Header().Set(mcpHeaderSessionID, testMCPSessionID)

		if sseInitialize {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":" + string(env.ID) +
				",\"result\":{\"protocolVersion\":\"2025-03-26\"}}\n\n"))

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(env.ID) +
			`,"result":{"protocolVersion":"2025-03-26","serverInfo":{"name":"solidping","version":"test"}}}`))
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = w.Write([]byte(": keep-alive\nevent: message\nid: 1\ndata: {\"jsonrpc\":\"2.0\",\"id\":" +
			string(env.ID) + ",\ndata: \"result\":{\"tools\":[{\"name\":\"list_checks\"}]}}\n\n"))
	case "echo":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(env.ID) + `,"result":` + string(env.Params) + `}`))
	case "tools/call":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\n  \"jsonrpc\": \"2.0\",\n  \"id\": " + string(env.ID) +
			",\n  \"result\": {\"content\": [{\"type\": \"text\", \"text\": \"2 checks\"}]}\n}\n"))
	default:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(env.ID) +
			`,"error":{"code":-32601,"message":"Method not found"}}`))
	}
}

// bridgeHarness runs a bridge wired to pipes, the way an MCP client drives
// `sp mcp` over the child process's stdin/stdout.
type bridgeHarness struct {
	t      *testing.T
	stdin  *io.PipeWriter
	stdout *bufio.Scanner
	done   chan error
}

func startBridge(t *testing.T, bridge *mcpBridge) *bridgeHarness {
	t.Helper()

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	bridge.out = outW
	if bridge.log == nil {
		bridge.log = slog.New(slog.DiscardHandler)
	}

	if bridge.httpClient == nil {
		bridge.httpClient = &http.Client{Timeout: testMCPTimeout}
	}

	stdout := bufio.NewScanner(outR)
	stdout.Buffer(make([]byte, 0, mcpReadBufferSize), 4*1024*1024)

	harness := &bridgeHarness{t: t, stdin: inW, stdout: stdout, done: make(chan error, 1)}

	go func() {
		harness.done <- bridge.run(context.Background(), inR)
		_ = outW.Close()
	}()

	t.Cleanup(func() {
		_ = inW.Close()
		_ = outR.Close()
	})

	return harness
}

func (h *bridgeHarness) send(line string) {
	h.t.Helper()

	_, err := io.WriteString(h.stdin, line+"\n")
	require.NoError(h.t, err)
}

// next returns the next stdout line decoded as one object.
func (h *bridgeHarness) next() map[string]any {
	h.t.Helper()

	line := h.nextRaw()

	var msg map[string]any
	require.NoError(h.t, json.Unmarshal([]byte(line), &msg), "every stdout line must be one JSON message: %q", line)

	return msg
}

// nextRaw returns the next stdout line, failing on a timeout.
func (h *bridgeHarness) nextRaw() string {
	h.t.Helper()

	lineCh := make(chan string, 1)

	go func() {
		if h.stdout.Scan() {
			lineCh <- h.stdout.Text()
		}

		close(lineCh)
	}()

	select {
	case line, ok := <-lineCh:
		require.True(h.t, ok, "stdout closed before a reply arrived")

		return line
	case <-time.After(testMCPTimeout):
		require.FailNow(h.t, "timed out waiting for a stdout line")

		return ""
	}
}

// closeAndWait sends EOF and waits for the bridge to exit, then asserts it
// wrote nothing more to stdout.
func (h *bridgeHarness) closeAndWait() {
	h.t.Helper()

	require.NoError(h.t, h.stdin.Close())

	select {
	case err := <-h.done:
		require.NoError(h.t, err)
	case <-time.After(testMCPTimeout):
		require.FailNow(h.t, "bridge did not exit on EOF")
	}

	require.False(h.t, h.stdout.Scan(), "nothing may reach stdout after EOF, got %q", h.stdout.Text())
}

// dig walks nested JSON objects along keys and returns the leaf as T.
func dig[T any](t *testing.T, value any, keys ...string) T {
	t.Helper()

	for _, key := range keys {
		obj, ok := value.(map[string]any)
		require.True(t, ok, "expected an object holding %q, got %v", key, value)

		value = obj[key]
	}

	typed, ok := value.(T)
	require.True(t, ok, "unexpected type %T for %v", value, keys)

	return typed
}

func errorOf(t *testing.T, msg map[string]any) map[string]any {
	t.Helper()

	return dig[map[string]any](t, msg, "error")
}

// TestMCPBridgeSessionLifecycle drives a whole session: initialize mints the
// session id, the notification produces no output, tools/list arrives as SSE,
// tools/call as multi-line JSON, and EOF closes the session with DELETE.
func TestMCPBridgeSessionLifecycle(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := newFakeMCPServer(t, "pat_good")
	h := startBridge(t, &mcpBridge{endpoint: fake.srv.URL + mcpAPIPath, token: "pat_good"})

	h.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	msg := h.next()
	r.InDelta(1, msg["id"], 0)
	r.Equal("solidping", dig[string](t, msg, "result", "serverInfo", "name"))

	h.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	h.send("") // blank lines are ignored, not forwarded
	h.send(`{"jsonrpc":"2.0","id":"two","method":"tools/list"}`)
	msg = h.next()
	r.Equal("two", msg["id"], "the notification wrote nothing, so the next line is the tools/list reply")
	tools := dig[[]any](t, msg, "result", "tools")
	r.Len(tools, 1)

	h.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_checks","arguments":{}}}`)
	msg = h.next()
	r.InDelta(3, msg["id"], 0)
	r.Contains(msg, "result")

	h.closeAndWait()

	seen := fake.requests()
	r.Len(seen, 5)

	r.Equal("initialize", seen[0].rpcMethod)
	r.Empty(seen[0].sessionID, "initialize must not carry a session id")

	for _, req := range seen {
		r.Equal("Bearer pat_good", req.authorization)
	}

	for _, req := range seen[1:] {
		r.Equal(testMCPSessionID, req.sessionID, "%s %s must carry the session id", req.httpMethod, req.rpcMethod)
	}

	r.Equal(http.MethodDelete, seen[4].httpMethod, "EOF must close the session")
}

// TestMCPBridgeUnauthorized: without a way to renew the credential, a 401 is
// turned into a JSON-RPC error for the request that caused it, naming the way
// out, and a 401 on a notification writes nothing at all.
func TestMCPBridgeUnauthorized(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := newFakeMCPServer(t, "pat_good")
	h := startBridge(t, &mcpBridge{endpoint: fake.srv.URL + mcpAPIPath, token: "pat_revoked"})

	h.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	h.send(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}`)

	msg := h.next()
	r.InDelta(7, msg["id"], 0, "the notification's 401 must not produce a line")

	errObj := errorOf(t, msg)
	r.InDelta(mcpCodeUnauthorized, errObj["code"], 0)
	r.Contains(errObj["message"], "HTTP 401")
	r.Contains(errObj["message"], "Invalid or expired token")
	r.Contains(errObj["message"], "sp auth login")

	data := dig[map[string]any](t, errObj, "data")
	r.InDelta(http.StatusUnauthorized, data["httpStatus"], 0)
	r.Equal("INVALID_TOKEN", data["code"])

	h.closeAndWait()

	for _, req := range fake.requests() {
		r.NotEqual(http.MethodDelete, req.httpMethod, "no session was minted, so there is nothing to close")
	}
}

// TestMCPBridgeRenewFailureBacksOff: when renewal fails (bad stored
// credentials), the next 401 goes straight back to the client instead of
// trying another login.
func TestMCPBridgeRenewFailureBacksOff(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := newFakeMCPServer(t, "pat_good")

	var renewCalls atomic.Int32

	h := startBridge(t, &mcpBridge{
		endpoint: fake.srv.URL + mcpAPIPath,
		token:    "pat_revoked",
		renew: func(context.Context, string) (string, error) {
			renewCalls.Add(1)

			return "", apihelper.ErrNoAuthentication
		},
	})

	for id := 1; id <= 2; id++ {
		h.send(`{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"tools/list"}`)
		msg := h.next()
		r.InDelta(mcpCodeUnauthorized, errorOf(t, msg)["code"], 0)
	}

	h.closeAndWait()

	r.Equal(int32(1), renewCalls.Load(), "a failed renewal must not be retried on the very next 401")
}

// signedTestJWT returns an unverified-parseable JWT expiring at exp, which is
// all the CLI's token bookkeeping reads.
func signedTestJWT(t *testing.T, subject string, exp time.Time) string {
	t.Helper()

	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": subject,
		"exp": exp.Unix(),
	}).SignedString([]byte("test-signing-key"))
	require.NoError(t, err)

	return token
}

// TestMCPBridgeRenewsExpiredSession wires the bridge to a real apihelper.Helper
// the way mcpAction does. The saved access token has expired; the server
// answers 401, the helper spends the stored refresh token, and the request is
// replayed with the new access token. The client never sees the 401.
func TestMCPBridgeRenewsExpiredSession(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	expired := signedTestJWT(t, "expired", time.Now().Add(-time.Minute))
	refresh := signedTestJWT(t, "refresh", time.Now().Add(time.Hour))
	fresh := signedTestJWT(t, "fresh", time.Now().Add(time.Hour))

	fake := newFakeMCPServer(t, fresh)

	var refreshCalls atomic.Int32

	fake.setExtra("/api/v1/auth/refresh", func(w http.ResponseWriter, req *http.Request) {
		refreshCalls.Add(1)

		var body struct {
			RefreshToken string `json:"refreshToken"`
		}

		_ = json.NewDecoder(req.Body).Decode(&body)
		if body.RefreshToken != refresh {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accessToken":"` + fresh + `","expiresIn":3600}`))
	})

	helper := apihelper.NewHelper(&config.Config{URL: fake.srv.URL, Org: "acme"},
		filepath.Join(t.TempDir(), "token.json"), false)
	r.NoError(helper.SaveTokens(expired, refresh))

	h := startBridge(t, &mcpBridge{
		endpoint: fake.srv.URL + mcpAPIPath,
		token:    expired,
		renew:    helper.RenewToken,
	})

	h.send(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	msg := h.next()
	r.Contains(msg, "result", "the 401 must be absorbed by the renewal, got %v", msg)

	h.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	msg = h.next()
	r.Contains(msg, "result")

	h.closeAndWait()

	r.Equal(int32(1), refreshCalls.Load(), "one refresh, then the new token is kept")

	seen := fake.requests()
	r.Equal("Bearer "+expired, seen[0].authorization)

	for _, req := range seen[1:] {
		r.Equal("Bearer "+fresh, req.authorization)
	}

	// The refreshed token is persisted, as for every other sp command.
	token, err := helper.Token(t.Context())
	r.NoError(err)
	r.Equal(fresh, token)
}

// TestMCPBridgeLocalErrors covers the replies the bridge writes on its own:
// a line that is not JSON, and a server that cannot be reached.
func TestMCPBridgeLocalErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	down := httptest.NewServer(http.NotFoundHandler())
	endpoint := down.URL + mcpAPIPath
	down.Close()

	h := startBridge(t, &mcpBridge{endpoint: endpoint, token: "pat_x"})

	h.send(`{"jsonrpc":"2.0","id":1,"method":`)
	msg := h.next()
	r.Nil(msg["id"])
	r.InDelta(mcpCodeParseError, errorOf(t, msg)["code"], 0)

	h.send(`{"jsonrpc":"2.0","id":"abc","method":"tools/list"}`)
	msg = h.next()
	r.Equal("abc", msg["id"])

	errObj := errorOf(t, msg)
	r.InDelta(mcpCodeInternal, errObj["code"], 0)
	r.Contains(errObj["message"], "unreachable")

	h.closeAndWait()
}

// TestMCPBridgeNonJSONRPCError: an error page that is not the REST shape (a
// reverse proxy, a wrong --url) still yields a JSON-RPC error, never raw bytes
// on stdout.
func TestMCPBridgeNonJSONRPCError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502 Bad Gateway</html>"))
	}))
	t.Cleanup(srv.Close)

	h := startBridge(t, &mcpBridge{endpoint: srv.URL + mcpAPIPath})

	h.send(`{"jsonrpc":"2.0","id":5,"method":"ping"}`)
	msg := h.next()
	r.InDelta(5, msg["id"], 0)

	errObj := errorOf(t, msg)
	r.InDelta(mcpCodeInternal, errObj["code"], 0)
	r.Contains(errObj["message"], "HTTP 502")

	h.closeAndWait()
}

// TestMCPBridgeScopeRefusal: the server refuses a token without the mcp scope
// with an id-less JSON-RPC error. A notification must get no reply at all,
// and a request must get the error with its own id stamped on.
func TestMCPBridgeScopeRefusal(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := newFakeMCPServer(t, "pat_good")
	fake.configure(func(f *fakeMCPServer) { f.noScope = "pat_noscope" })

	h := startBridge(t, &mcpBridge{endpoint: fake.srv.URL + mcpAPIPath, token: "pat_noscope"})

	h.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	h.send(`{"jsonrpc":"2.0","id":9,"method":"tools/list"}`)

	msg := h.next()
	r.InDelta(9, msg["id"], 0, "the notification's refusal must not produce a line, and the request's carries its id")
	r.InDelta(-32002, errorOf(t, msg)["code"], 0)
	r.Contains(errorOf(t, msg)["message"], "mcp")

	h.send(`{"jsonrpc":"2.0","id":"s-1","method":"tools/call"}`)
	r.Equal("s-1", h.next()["id"], "string ids are stamped as sent")

	h.closeAndWait()
	r.Len(fake.requests(), 3)
}

// TestMCPBridgeBatch: a batch is relayed member by member and answered with
// one array of the requests' replies. Notifications add nothing, and a batch
// of only notifications gets no line at all.
func TestMCPBridgeBatch(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := newFakeMCPServer(t, "pat_good")
	h := startBridge(t, &mcpBridge{endpoint: fake.srv.URL + mcpAPIPath, token: "pat_good"})

	h.send(`[{"jsonrpc":"2.0","method":"notifications/initialized"}]`)
	h.send(`[{"jsonrpc":"2.0","id":1,"method":"tools/list"},` +
		`{"jsonrpc":"2.0","method":"notifications/initialized"},` +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call"}]`)

	line := h.nextRaw()

	var replies []map[string]any
	r.NoError(json.Unmarshal([]byte(line), &replies), "a batch is answered with one array: %s", line)
	r.Len(replies, 2)
	r.InDelta(1, replies[0]["id"], 0)
	r.InDelta(2, replies[1]["id"], 0)

	h.send(`[]`)
	msg := h.next()
	r.Nil(msg["id"])
	r.InDelta(mcpCodeInvalidRequest, errorOf(t, msg)["code"], 0)

	h.closeAndWait()

	for _, req := range fake.requests() {
		r.NotEmpty(req.rpcMethod, "every member is POSTed on its own, never the array")
	}
}

// TestMCPBridgeSSEInitializeSession: the session id is captured when the
// initialize reply is an event stream, and sent on the next request.
func TestMCPBridgeSSEInitializeSession(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := newFakeMCPServer(t, "pat_good")
	fake.configure(func(f *fakeMCPServer) { f.sseInitialize = true })

	h := startBridge(t, &mcpBridge{endpoint: fake.srv.URL + mcpAPIPath, token: "pat_good"})

	h.send(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	r.Equal("2025-03-26", dig[string](t, h.next(), "result", "protocolVersion"))

	h.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	r.InDelta(2, h.next()["id"], 0)

	h.closeAndWait()

	seen := fake.requests()
	r.Len(seen, 3)
	r.Equal(testMCPSessionID, seen[1].sessionID)
	r.Equal(http.MethodDelete, seen[2].httpMethod)
	r.Equal(testMCPSessionID, seen[2].sessionID)
}

// TestMCPBridgeLongLine: a stdin line well past the 64 KiB read buffer is
// relayed intact, and so is the equally long reply.
func TestMCPBridgeLongLine(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := newFakeMCPServer(t, "pat_good")
	h := startBridge(t, &mcpBridge{endpoint: fake.srv.URL + mcpAPIPath, token: "pat_good"})

	payload := strings.Repeat("0123456789abcdef", 12*1024) // 192 KiB
	h.send(`{"jsonrpc":"2.0","id":1,"method":"echo","params":{"blob":"` + payload + `"}}`)

	msg := h.next()
	r.Equal(payload, dig[string](t, msg, "result", "blob"))

	h.closeAndWait()
}

// TestMCPFailureExitsQuietly: a failure is logged to the bridge's logger and
// becomes an exit code with an empty message, so urfave exits by itself and
// nothing is handed back to a main that would log it on stdout.
func TestMCPFailureExitsQuietly(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var logs strings.Builder

	logger := slog.New(slog.NewTextHandler(&logs, nil))

	err := mcpFailure(t.Context(), logger, io.ErrUnexpectedEOF)

	var exitCoder cli.ExitCoder
	r.ErrorAs(err, &exitCoder)
	r.Equal(1, exitCoder.ExitCode())
	r.Empty(err.Error(), "a non-empty message would be printed by urfave")
	r.Contains(logs.String(), "unexpected EOF")
}

func TestIsJSONRPC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "response", body: `{"jsonrpc":"2.0","id":1,"result":{}}`, want: true},
		{name: "batch", body: `[{"jsonrpc":"2.0","id":1,"result":{}}]`, want: false},
		{name: "rest error", body: `{"title":"Invalid or expired token","code":"INVALID_TOKEN"}`, want: false},
		{name: "wrong version", body: `{"jsonrpc":"1.0","id":1}`, want: false},
		{name: "html", body: `<html></html>`, want: false},
		{name: "empty", body: "  ", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, isJSONRPC([]byte(tt.body)))
		})
	}
}

// TestMCPCommandRegistered pins the command name and its token flag, which the
// docs' Claude Desktop snippet depends on.
func TestMCPCommandRegistered(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var found bool

	for _, cmd := range GetCommands() {
		if cmd.Name != "mcp" {
			continue
		}

		found = true

		var hasToken bool

		for _, flag := range cmd.Flags {
			if strings.Contains(strings.Join(flag.Names(), ","), flagToken) {
				hasToken = true
			}
		}

		r.True(hasToken, "sp mcp must accept --token / SP_TOKEN")
	}

	r.True(found, "sp mcp must be registered")
}
