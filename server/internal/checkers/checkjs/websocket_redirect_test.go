package checkjs

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const wsRedirectSecret = "ws-s3cr3t-token"

// wsHeaderRecorder is a WebSocket server that keeps the handshake headers it
// received and echoes X-Api-Key back as the first frame.
type wsHeaderRecorder struct {
	mu      sync.Mutex
	headers []http.Header
}

func (rec *wsHeaderRecorder) seen() []http.Header {
	rec.mu.Lock()
	defer rec.mu.Unlock()

	return append([]http.Header(nil), rec.headers...)
}

func startWSHeaderRecorder(t *testing.T) (*httptest.Server, *wsHeaderRecorder) {
	t.Helper()

	rec := &wsHeaderRecorder{}

	server := startWSFixture(t, func(conn *websocket.Conn, req *http.Request) {
		rec.mu.Lock()
		rec.headers = append(rec.headers, req.Header.Clone())
		rec.mu.Unlock()

		_ = conn.Write(req.Context(), websocket.MessageText, []byte("key="+req.Header.Get("X-Api-Key")))

		<-req.Context().Done()
	})

	return server, rec
}

// wsRedirectingServer answers every request with a 302 to target.
func wsRedirectingServer(t *testing.T, target string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, target, http.StatusFound)
	}))
	t.Cleanup(server.Close)

	return server
}

// wsRedirectScript connects to env.URL with headers carrying the secret.
const wsRedirectScript = `
var ws = websocket.connect(env.URL, { headers: {
  "X-Api-Key": secrets.TOKEN,
  "Authorization": "Bearer " + secrets.TOKEN,
}});
if (!ws.ok) { return { status: "down", output: { error: ws.error, dropped: ws.redirectDroppedHeaders } }; }
var frame = ws.receive({ timeout: "5s" });
ws.close();
return { status: "up", output: { seen: frame.data, dropped: ws.redirectDroppedHeaders } };
`

// TestWebSocketRedirectCrossOriginDropsSecrets: a handshake redirected to
// another origin reaches it without the secret-bearing headers, Authorization
// or Referer, and the check names what was dropped (spec 2026-11-16-03).
func TestWebSocketRedirectCrossOriginDropsSecrets(t *testing.T) {
	t.Parallel()

	target, rec := startWSHeaderRecorder(t)
	origin := wsRedirectingServer(t, wsURL(target)+"/landing")

	results := runBothPaths(t, wsRedirectScript,
		map[string]string{"URL": wsURL(origin) + "/start"},
		map[string]string{"TOKEN": wsRedirectSecret})

	for name, result := range results {
		r := require.New(t)

		r.Equal("up", result.Status.String(), "%s: %#v", name, result.Output)
		r.Equal("key=", result.Output["seen"], name)
		r.ElementsMatch([]any{"Authorization", "X-Api-Key"}, result.Output["dropped"], name)
		r.ElementsMatch([]string{"Authorization", "X-Api-Key"},
			result.Output[OutputKeyRedirectDroppedSecretHeaders], name)
	}

	seen := rec.seen()
	require.Len(t, seen, len(results))

	for _, header := range seen {
		require.Empty(t, header.Get("X-Api-Key"))
		require.Empty(t, header.Get("Authorization"))
		require.Empty(t, header.Get("Referer"))

		for _, values := range header {
			for _, value := range values {
				require.NotContains(t, value, wsRedirectSecret)
			}
		}
	}
}

// TestWebSocketRedirectSameOriginKeepsHeaders is the positive control: a hop
// on the same origin keeps the script's headers and the handshake succeeds.
func TestWebSocketRedirectSameOriginKeepsHeaders(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var rec wsHeaderRecorder

	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/ws", http.StatusFound)
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}

		defer func() { _ = conn.CloseNow() }()

		rec.mu.Lock()
		rec.headers = append(rec.headers, req.Header.Clone())
		rec.mu.Unlock()

		_ = conn.Write(req.Context(), websocket.MessageText, []byte("key="+req.Header.Get("X-Api-Key")))

		<-req.Context().Done()
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	results := runBothPaths(t, wsRedirectScript,
		map[string]string{"URL": wsURL(server) + "/start"},
		map[string]string{"TOKEN": wsRedirectSecret})

	for name, result := range results {
		r.Equal("up", result.Status.String(), "%s: %#v", name, result.Output)
		r.Equal("key="+wsRedirectSecret, result.Output["seen"], name)
		r.Nil(result.Output["dropped"], name)
		r.NotContains(result.Output, OutputKeyRedirectDroppedSecretHeaders, name)
	}

	for _, header := range rec.seen() {
		r.Equal("Bearer "+wsRedirectSecret, header.Get("Authorization"))
	}
}

// TestWebSocketRedirectLoopIsCapped: a handshake redirected forever stops at
// checkerdef.DefaultMaxRedirects with a redirect-limit error, long before the
// check's timeout.
func TestWebSocketRedirectLoopIsCapped(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var hits atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hits.Add(1)
		http.Redirect(w, req, "/again", http.StatusFound)
	}))
	t.Cleanup(server.Close)

	start := time.Now()
	result := runSocketScript(t, `
var ws = websocket.connect("`+wsURL(server)+`/start");
return { status: ws.ok ? "up" : "down", output: { error: ws.error } };
`, 30*time.Second)

	r.Equal("down", result.Status.String(), "output: %#v", result.Output)
	r.Contains(result.Output["error"], checkerdef.ErrTooManyRedirects.Error())
	r.EqualValues(checkerdef.DefaultMaxRedirects, hits.Load())
	r.Less(time.Since(start), 10*time.Second)
}

// TestWebSocketRedirectRefusesDowngrade: a wss handshake redirected to ws on
// the same host is refused, and the ws server is never reached.
func TestWebSocketRedirectRefusesDowngrade(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	plain, rec := startWSHeaderRecorder(t)

	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, wsURL(plain)+"/clear", http.StatusFound)
	}))
	t.Cleanup(secure.Close)

	secureURL, err := url.Parse(secure.URL)
	r.NoError(err)
	plainURL, err := url.Parse(plain.URL)
	r.NoError(err)
	r.Equal(secureURL.Hostname(), plainURL.Hostname(), "both servers share the host")

	result := runSocketScript(t, `
var ws = websocket.connect("`+strings.Replace(secure.URL, "https://", "wss://", 1)+`/start", { tlsVerify: false });
return { status: ws.ok ? "up" : "down", output: { error: ws.error } };
`, 15*time.Second)

	r.Equal("down", result.Status.String(), "output: %#v", result.Output)
	r.Contains(result.Output["error"], errWSRedirectDowngrade.Error())
	r.Empty(rec.seen())
}
