package middleware_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/middleware"
)

func TestRequestTimeout_Slow504(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	slow := func(w http.ResponseWriter, req bunrouter.Request) error {
		select {
		case <-time.After(500 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
		case <-req.Context().Done():
		}
		return nil
	}
	handler := middleware.RequestTimeout(50 * time.Millisecond)(slow)

	w := httptest.NewRecorder()
	_ = handler(w, newBunRequest("30.0.0.1", "/api/v1/orgs/default/checks"))
	r.Equal(http.StatusGatewayTimeout, w.Code)
	r.Contains(w.Body.String(), "REQUEST_TIMEOUT")
}

func TestRequestTimeout_FastUnaffected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := middleware.RequestTimeout(500 * time.Millisecond)(okHandler())

	w := httptest.NewRecorder()
	_ = handler(w, newBunRequest("30.0.0.2", "/api/v1/orgs/default/checks"))
	r.Equal(http.StatusOK, w.Code)
}

// Regression: handlers that call WriteHeader followed by Write (the pattern
// every JSON response uses) must reach the wire intact. A previous version
// of timeoutWriter shared one `claimed` flag between WriteHeader and Write,
// which caused WriteHeader to claim and Write to be silently swallowed —
// 200 OK with an empty body and no JSON.
func TestRequestTimeout_WriteHeaderThenWriteBodyReachesWire(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	jsonHandler := func(w http.ResponseWriter, _ bunrouter.Request) error {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"accessToken":"abc"}`))
		return nil
	}
	handler := middleware.RequestTimeout(500 * time.Millisecond)(jsonHandler)

	w := httptest.NewRecorder()
	_ = handler(w, newBunRequest("30.0.0.5", "/api/v1/orgs/default/checks"))
	r.Equal(http.StatusOK, w.Code)
	r.JSONEq(`{"accessToken":"abc"}`, w.Body.String())
}

// Regression: a handler that times out keeps running in the background and may
// still touch the response writer's headers (e.g. the rate-limiter writes
// Content-Type / Retry-After on its 429 path) at the same moment the timeout
// middleware writes its 504. Both used to mutate the *same* http.Header map,
// which the Go runtime aborts with "fatal error: concurrent map writes" and
// crashed the whole server. The handler must now own a private header map, so
// this races cleanly under `go test -race`.
func TestRequestTimeout_HandlerHeaderWriteAfterTimeoutDoesNotRace(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	released := make(chan struct{})
	racing := func(w http.ResponseWriter, req bunrouter.Request) error {
		<-req.Context().Done() // wait until the middleware has timed us out
		// Hammer the header map the way a late rate-limiter rejection would,
		// concurrently with the middleware committing its 504.
		for i := 0; i < 500; i++ {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":"RATE_LIMITED"}`))
		}
		close(released)
		return nil
	}
	handler := middleware.RequestTimeout(20 * time.Millisecond)(racing)

	w := httptest.NewRecorder()
	// While the request is in flight the middleware writes the 504 on the main
	// goroutine; the handler concurrently mutates headers on the background one.
	// The point of the test is that this does not trigger a concurrent map
	// write — the run is executed under `-race` in CI.
	_ = handler(w, newBunRequest("30.0.0.6", "/api/v1/orgs/default/checks"))
	<-released // ensure the racing handler has finished before the test ends

	// Whichever side won the claim, the client sees exactly one coherent
	// response (never a corrupted mix), and the runtime did not abort.
	r.Contains(
		[]int{http.StatusGatewayTimeout, http.StatusTooManyRequests},
		w.Code,
	)
}

// Regression: a handler that commits its response BEFORE the deadline and is
// still writing when the deadline fires must be allowed to finish. A previous
// version returned from the middleware at the deadline regardless — net/http
// then recycled the connection's write buffers under the in-flight write,
// which SIGSEGV'd the whole server (bufio.Writer nil dereference, observed in
// CI E2E as every subsequent request getting ERR_CONNECTION_REFUSED).
func TestRequestTimeout_CommittedResponseStreamsToCompletion(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var handlerDone atomic.Bool
	streaming := func(w http.ResponseWriter, _ bunrouter.Request) error {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("part-1;"))
		time.Sleep(60 * time.Millisecond) // 20ms deadline fires mid-stream
		_, _ = w.Write([]byte("part-2"))
		handlerDone.Store(true)
		return nil
	}
	handler := middleware.RequestTimeout(20 * time.Millisecond)(streaming)

	w := httptest.NewRecorder()
	_ = handler(w, newBunRequest("30.0.0.7", "/api/v1/orgs/default/checks"))

	r.True(handlerDone.Load(),
		"middleware must not return while the handler is still streaming a committed response")
	r.Equal(http.StatusOK, w.Code, "a committed 200 must never be replaced by a 504")
	r.Equal("part-1;part-2", w.Body.String(), "the full body must reach the wire")
}

// Same regression, proven at the connection level: with the pre-fix middleware
// this test does not merely fail — the late write hits the recycled bufio
// writer of a real net/http connection and the unrecovered panic kills the
// test process, exactly how the server died in CI.
func TestRequestTimeout_RealServerLateStreamDoesNotCrash(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const lateChunk = 64 * 1024

	router := bunrouter.New()
	router.GET("/api/v1/orgs/:org/checks",
		middleware.RequestTimeout(30*time.Millisecond)(func(w http.ResponseWriter, _ bunrouter.Request) error {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("head;"))
			time.Sleep(90 * time.Millisecond) // outlive the deadline mid-response
			_, _ = w.Write(bytes.Repeat([]byte("x"), lateChunk))
			return nil
		}))

	srv := httptest.NewServer(router)
	defer srv.Close()

	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, srv.URL+"/api/v1/orgs/default/checks", nil)
	r.NoError(err)
	resp, err := srv.Client().Do(req)
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)
	r.Equal(http.StatusOK, resp.StatusCode)
	r.Len(body, len("head;")+lateChunk, "client must receive the complete streamed body")
}

func TestRequestTimeout_Disabled(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	slow := func(w http.ResponseWriter, _ bunrouter.Request) error {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		return nil
	}
	handler := middleware.RequestTimeout(0)(slow)

	w := httptest.NewRecorder()
	_ = handler(w, newBunRequest("30.0.0.3", "/api/v1/orgs/default/checks"))
	r.Equal(http.StatusOK, w.Code, "duration=0 should disable the middleware")
}

func TestRequestTimeout_ExcludedPaths(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	slow := func(w http.ResponseWriter, _ bunrouter.Request) error {
		time.Sleep(80 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		return nil
	}
	handler := middleware.RequestTimeout(20 * time.Millisecond)(slow)

	for _, path := range []string{
		"/api/mgmt/health",
		"/metrics",
		"/api/v1/agent/ws",
		"/api/v1/heartbeat/org/identifier",
		"/api/v1/orgs/default/events/ws",
	} {
		w := httptest.NewRecorder()
		_ = handler(w, newBunRequest("30.0.0.4", path))
		r.Equal(http.StatusOK, w.Code, "excluded path %s must bypass the timeout", path)
	}
}
