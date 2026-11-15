package checkwebsocket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// headerRecorder is a WebSocket server that records the handshake headers.
type headerRecorder struct {
	mu     sync.Mutex
	header http.Header
}

func (h *headerRecorder) last() http.Header {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.header
}

func (h *headerRecorder) accept(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.header = r.Header.Clone()
	h.mu.Unlock()

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}

	_ = conn.Close(websocket.StatusNormalClosure, "")
}

//nolint:gochecknoglobals // read-only test fixture
var redirectTestHeaders = map[string]string{"X-Api-Key": "k", "Authorization": "Bearer t"}

// TestWebSocketChecker_CrossOriginRedirectDropsHeaders: a handshake
// redirected to another origin does not carry the configured headers
// (spec 2026-11-15-02).
func TestWebSocketChecker_CrossOriginRedirectDropsHeaders(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	recorder := &headerRecorder{}
	target := httptest.NewServer(http.HandlerFunc(recorder.accept))
	t.Cleanup(target.Close)

	targetWS := "ws" + strings.TrimPrefix(target.URL, "http") + "/ws"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, targetWS, http.StatusFound)
	}))
	t.Cleanup(origin.Close)

	result, err := (&WebSocketChecker{}).Execute(t.Context(), &WebSocketConfig{
		URL:     "ws" + strings.TrimPrefix(origin.URL, "http") + "/ws?token=q",
		Headers: redirectTestHeaders,
	})
	r.NoError(err)

	header := recorder.last()
	r.NotNil(header, "the redirect target must have been reached")
	r.Empty(header.Get("X-Api-Key"))
	r.Empty(header.Get("Authorization"))
	r.Empty(header.Get("Referer"))

	r.Equal(checkerdef.StatusUp, result.Status)
	r.Equal([]string{"Authorization", "X-Api-Key"},
		result.Output[checkerdef.OutputKeyRedirectDroppedSecretHeaders])
}

// TestWebSocketChecker_SameOriginRedirectKeepsHeaders is the positive control.
func TestWebSocketChecker_SameOriginRedirectKeepsHeaders(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	recorder := &headerRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/real" {
			http.Redirect(w, req, "/real", http.StatusFound)

			return
		}

		recorder.accept(w, req)
	}))
	t.Cleanup(server.Close)

	result, err := (&WebSocketChecker{}).Execute(t.Context(), &WebSocketConfig{
		URL:     "ws" + strings.TrimPrefix(server.URL, "http") + "/ws",
		Headers: redirectTestHeaders,
	})
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status)
	r.NotContains(result.Output, checkerdef.OutputKeyRedirectDroppedSecretHeaders)

	header := recorder.last()
	r.NotNil(header)
	r.Equal("k", header.Get("X-Api-Key"))
	r.Equal("Bearer t", header.Get("Authorization"))
}

// TestWebSocketChecker_RedirectLoopIsCapped: coder/websocket alone follows a
// loop until the check times out; the policy ends it after the cap.
func TestWebSocketChecker_RedirectLoopIsCapped(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var hits atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hits.Add(1)
		http.Redirect(w, req, "/loop", http.StatusFound)
	}))
	t.Cleanup(server.Close)

	start := time.Now()
	result, err := (&WebSocketChecker{}).Execute(t.Context(), &WebSocketConfig{
		URL:     "ws" + strings.TrimPrefix(server.URL, "http") + "/ws",
		Timeout: 5 * time.Second,
	})
	r.NoError(err)

	r.Equal(checkerdef.StatusDown, result.Status)
	r.Contains(result.Output["error"], "too many redirects")
	r.Less(time.Since(start), 5*time.Second)
	r.EqualValues(checkerdef.DefaultMaxRedirects, hits.Load())
}
