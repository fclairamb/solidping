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
