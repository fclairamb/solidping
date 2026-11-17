package checkbrowser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// fakeBrowser is a scripted DevTools endpoint: it speaks just enough of the
// protocol for chromedp to open a page, and answers every other command from a
// handler table (an empty result by default). It lets the selector, Basic auth
// and session code run in an environment with no Chrome at all, which is what
// CI is.
type fakeBrowser struct {
	t      *testing.T
	server *httptest.Server

	mu       sync.Mutex
	methods  map[string]func(params json.RawMessage) (any, *fakeCDPError)
	evaluate func(expr string) (any, string)
	calls    []string
	conns    []*websocket.Conn
}

// fakeCDPError is a protocol error answer.
type fakeCDPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const fakePageSession = "fake-session"

func newFakeBrowser(t *testing.T) *fakeBrowser {
	t.Helper()

	fake := &fakeBrowser{t: t, methods: map[string]func(json.RawMessage) (any, *fakeCDPError){}}

	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Browser":"HeadlessChrome/140.0.0.0",` +
			`"webSocketDebuggerUrl":"ws://` + req.Host + `/devtools/browser/fake"}`))
	})
	mux.HandleFunc("/devtools/browser/fake", fake.serveSocket)

	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)

	return fake
}

// url is the CDP endpoint to configure.
func (f *fakeBrowser) url() string { return f.server.URL }

// on scripts the answer to one protocol method.
func (f *fakeBrowser) on(method string, fn func(params json.RawMessage) (any, *fakeCDPError)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.methods[method] = fn
}

// onEvaluate scripts Runtime.evaluate: fn returns the value, or an exception
// text when it is not empty.
func (f *fakeBrowser) onEvaluate(fn func(expr string) (any, string)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.evaluate = fn
}

// called reports how many times a method was received.
func (f *fakeBrowser) called(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	count := 0

	for _, call := range f.calls {
		if call == method {
			count++
		}
	}

	return count
}

// emit pushes a protocol event on the page session.
func (f *fakeBrowser) emit(method string, params any) {
	f.t.Helper()

	raw, err := json.Marshal(map[string]any{"method": method, "params": params, "sessionId": fakePageSession})
	require.NoError(f.t, err)

	f.mu.Lock()
	conns := append([]*websocket.Conn(nil), f.conns...)
	f.mu.Unlock()

	for _, conn := range conns {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = conn.Write(ctx, websocket.MessageText, raw)

		cancel()
	}
}

func (f *fakeBrowser) serveSocket(w http.ResponseWriter, req *http.Request) {
	conn, err := websocket.Accept(w, req, nil)
	if err != nil {
		return
	}

	conn.SetReadLimit(1 << 24)

	f.mu.Lock()
	f.conns = append(f.conns, conn)
	f.mu.Unlock()

	defer func() { _ = conn.CloseNow() }()

	for {
		_, data, readErr := conn.Read(req.Context())
		if readErr != nil {
			return
		}

		var msg struct {
			ID        int64           `json:"id"`
			Method    string          `json:"method"`
			Params    json.RawMessage `json:"params"`
			SessionID string          `json:"sessionId"`
		}

		if json.Unmarshal(data, &msg) != nil {
			continue
		}

		result, cdpErr := f.answer(msg.Method, msg.Params)

		reply := map[string]any{"id": msg.ID}
		if msg.SessionID != "" {
			reply["sessionId"] = msg.SessionID
		}

		if cdpErr != nil {
			reply["error"] = cdpErr
		} else {
			reply["result"] = result
		}

		raw, _ := json.Marshal(reply) //nolint:errchkjson // test fake: replies are plain maps
		if conn.Write(req.Context(), websocket.MessageText, raw) != nil {
			return
		}
	}
}

func (f *fakeBrowser) answer(method string, params json.RawMessage) (any, *fakeCDPError) {
	f.mu.Lock()
	f.calls = append(f.calls, method)
	handler := f.methods[method]
	evaluate := f.evaluate
	f.mu.Unlock()

	if handler != nil {
		return handler(params)
	}

	switch method {
	case "Target.createTarget":
		return map[string]any{"targetId": "fake-target"}, nil
	case "Target.attachToTarget":
		return map[string]any{"sessionId": fakePageSession}, nil
	case "Runtime.evaluate":
		return f.answerEvaluate(params, evaluate), nil
	case "Runtime.callFunctionOn":
		return map[string]any{"result": map[string]any{"type": "undefined"}}, nil
	case "DOM.resolveNode":
		return map[string]any{"object": map[string]any{"type": "object", "objectId": "node-1"}}, nil
	case "Accessibility.queryAXTree":
		return map[string]any{"nodes": []any{}}, nil
	default:
		return map[string]any{}, nil
	}
}

func (f *fakeBrowser) answerEvaluate(params json.RawMessage, evaluate func(string) (any, string)) any {
	var in struct {
		Expression string `json:"expression"`
	}

	_ = json.Unmarshal(params, &in)

	if evaluate != nil {
		value, exception := evaluate(in.Expression)
		if exception != "" {
			return map[string]any{
				"result":           map[string]any{"type": "object"},
				"exceptionDetails": map[string]any{"exceptionId": 1, "text": exception, "lineNumber": 0, "columnNumber": 0},
			}
		}

		if value != nil {
			return map[string]any{"result": map[string]any{"type": "object", "value": value}}
		}
	}

	if strings.HasPrefix(in.Expression, "self") {
		return map[string]any{"result": map[string]any{"type": "object", "className": "Window"}}
	}

	if in.Expression == "document" {
		return map[string]any{"result": map[string]any{"type": "object", "objectId": "doc-1"}}
	}

	return map[string]any{"result": map[string]any{"type": "undefined"}}
}

// openFakeSession opens a Session over a fake browser.
func openFakeSession(t *testing.T, fake *fakeBrowser) (*Session, context.Context) {
	t.Helper()

	withSettings(t, Settings{CDPURL: fake.url()})

	ctx, cancel := contextWithTimeout(t, 30*time.Second)
	t.Cleanup(cancel)

	session, err := Open(ctx)
	require.NoError(t, err)
	t.Cleanup(session.Close)

	return session, ctx
}
