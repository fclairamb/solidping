package middleware_test

import (
	"net/http"
	"net/http/httptest"
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
		"/api/v1/workers/heartbeat",
		"/api/v1/heartbeat/org/identifier",
	} {
		w := httptest.NewRecorder()
		_ = handler(w, newBunRequest("30.0.0.4", path))
		r.Equal(http.StatusOK, w.Code, "excluded path %s must bypass the timeout", path)
	}
}
