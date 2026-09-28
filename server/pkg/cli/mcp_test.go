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
	f.mu.Unlock()

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
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(env.ID) +
			`,"result":{"protocolVersion":"2025-03-26","serverInfo":{"name":"solidping","version":"test"}}}`))
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = w.Write([]byte(": keep-alive\nevent: message\nid: 1\ndata: {\"jsonrpc\":\"2.0\",\"id\":" +
			string(env.ID) + ",\ndata: \"result\":{\"tools\":[{\"name\":\"list_checks\"}]}}\n\n"))
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

	harness := &bridgeHarness{t: t, stdin: inW, stdout: bufio.NewScanner(outR), done: make(chan error, 1)}

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

// next returns the next stdout line decoded, failing on a timeout.
func (h *bridgeHarness) next() map[string]any {
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

		var msg map[string]any
		require.NoError(h.t, json.Unmarshal([]byte(line), &msg), "every stdout line must be one JSON message: %q", line)

		return msg
	case <-time.After(testMCPTimeout):
		require.FailNow(h.t, "timed out waiting for a stdout line")

		return nil
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

func TestIsJSONRPC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "response", body: `{"jsonrpc":"2.0","id":1,"result":{}}`, want: true},
		{name: "batch", body: `[{"jsonrpc":"2.0","id":1,"result":{}}]`, want: true},
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
