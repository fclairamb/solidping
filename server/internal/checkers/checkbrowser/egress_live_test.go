package checkbrowser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/egress"
)

// wsPage opens a WebSocket to the URL in its "ws" query parameter and writes
// the first frame into the title, so a script reading the page sees it.
const wsPage = `<!doctype html><html><head><title>waiting</title></head><body>
<p data-testid="ws-page">ws</p>
<script>
const target = new URLSearchParams(location.search).get("ws");
const ws = new WebSocket(target);
ws.onmessage = (e) => { document.title = "got:" + e.data; };
ws.onerror = () => { document.title = "error"; };
</script></body></html>`

// wsFrame is what both WebSocket servers send to a client that connects.
const wsFrame = "internal-secret"

// wsServer serves wsPage on / and a WebSocket sending wsFrame on /ws, on the
// given loopback address, and counts the handshakes it received.
func wsServer(t *testing.T, addr string) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var handshakes atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(wsPage))
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handshakes.Add(1)

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
		if err != nil {
			return
		}

		defer func() { _ = conn.CloseNow() }()

		_ = conn.Write(r.Context(), websocket.MessageText, []byte(wsFrame))

		<-r.Context().Done()
	})

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", addr)
	if err != nil {
		t.Skipf("cannot listen on %s: %v", addr, err)
	}

	server := httptest.NewUnstartedServer(mux)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	return server, &handshakes
}

// TestBrowserWebSocketToAPrivateHostIsRefused is spec 2026-11-19-04's
// regression: a page served from a host the guard treats as public opens a
// WebSocket to a listener the guard classes as private. The internal listener
// must never see the handshake, and the script must get the egress refusal,
// never the frame. The positive control opens a WebSocket to an allowed host
// and reads its frame.
//
// The "public" side is the IPv6 loopback with egress.WithPublicOverride (a
// test cannot reach a real public address), the private side is 127.0.0.1,
// judged exactly as production would.
//
//nolint:paralleltest // mutates the process-wide settings
func TestBrowserWebSocketToAPrivateHostIsRefused(t *testing.T) {
	settings, ok := liveBrowserSettings()
	if !ok {
		t.Skip(liveBrowserSkipReason)
	}

	if !proxyReachableFrom(settings) {
		t.Skip("the remote Chrome cannot reach this process's loopback: the egress proxy is not in play")
	}

	withSettings(t, settings)
	widenChromeStart(t)

	public, publicHandshakes := wsServer(t, "[::1]:0")
	private, privateHandshakes := wsServer(t, "127.0.0.1:0")

	wsURL := func(srv string) string {
		return "ws://" + strings.TrimPrefix(srv, "http://") + "/ws"
	}

	guard := egress.New(false, egress.WithPublicOverride(net.ParseIP("::1")))

	open := func(t *testing.T) (context.Context, *Session, *egress.Recorder) {
		t.Helper()

		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		t.Cleanup(cancel)

		ctx, rec := egress.WithRecorder(egress.WithGuard(ctx, guard))

		session, err := Open(ctx)
		require.NoError(t, err)
		t.Cleanup(session.Close)

		require.NotNil(t, session.proxy, "an enforcing session goes through the egress proxy")

		return ctx, session, rec
	}

	t.Run("allowed host: the frame is readable", func(t *testing.T) {
		r := require.New(t)
		ctx, session, _ := open(t)

		pageURL := public.URL + "/?ws=" + url.QueryEscape(wsURL(public.URL))
		_, err := session.Navigate(ctx, pageURL)
		r.NoError(err)

		title, err := pollTitle(ctx, session, func(title string) bool { return title != "waiting" })
		r.NoError(err)
		r.Equal("got:"+wsFrame, title)
		r.Positive(publicHandshakes.Load())
	})

	t.Run("named hosts resolve through the guard, not Chrome", func(t *testing.T) {
		r := require.New(t)

		// Only the guard knows these names, so the page and its socket load
		// only if every connection really goes through the proxy: Chrome's
		// own resolver maps everything but the pinned host to NOTFOUND.
		named := egress.New(false, egress.WithPublicOverride(net.ParseIP("::1")), egress.WithLookup(
			func(_ context.Context, host string) ([]net.IPAddr, error) {
				if strings.HasSuffix(host, ".acme.test") {
					return []net.IPAddr{{IP: net.ParseIP("::1")}}, nil
				}

				return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
			},
		))

		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		defer cancel()

		ctx = egress.WithGuard(ctx, named)

		_, port, err := net.SplitHostPort(strings.TrimPrefix(public.URL, "http://"))
		r.NoError(err)

		pageURL := "http://www.acme.test:" + port + "/?ws=" + url.QueryEscape("ws://ws.acme.test:"+port+"/ws")

		pin, err := preflightEgress(ctx, pageURL)
		r.NoError(err)
		r.NotNil(pin)

		session, err := openSessionPinned(ctx, ctx, pin)
		r.NoError(err)

		defer session.Close()

		_, err = session.Navigate(ctx, pageURL)
		r.NoError(err)

		title, err := pollTitle(ctx, session, func(title string) bool { return title != "waiting" })
		r.NoError(err)
		r.Equal("got:"+wsFrame, title)
	})

	t.Run("private host: refused before the handshake", func(t *testing.T) {
		r := require.New(t)
		ctx, session, rec := open(t)

		pageURL := public.URL + "/?ws=" + url.QueryEscape(wsURL(private.URL))
		_, err := session.Navigate(ctx, pageURL)
		if err == nil {
			_, err = pollTitle(ctx, session, func(title string) bool { return strings.HasPrefix(title, "got:") })
		}

		r.ErrorIs(err, egress.ErrDenied, "the script gets the refusal, never the frame")
		r.NotNil(rec.Denied(), "the refusal is on the execution's recorder")
		r.Zero(privateHandshakes.Load(), "the internal listener never saw a handshake")
	})
}

var errTitleNeverSettled = errors.New("the page title never settled")

// pollTitle reads the page title until done accepts it, an action fails or
// ctx ends.
func pollTitle(ctx context.Context, session *Session, done func(string) bool) (string, error) {
	deadline := time.Now().Add(15 * time.Second)

	for {
		value, err := session.Evaluate(ctx, "document.title")
		if err != nil {
			return "", err
		}

		title, _ := value.(string)
		if done(title) {
			return title, nil
		}

		if time.Now().After(deadline) {
			return title, fmt.Errorf("%w: %q", errTitleNeverSettled, title)
		}

		time.Sleep(100 * time.Millisecond)
	}
}
