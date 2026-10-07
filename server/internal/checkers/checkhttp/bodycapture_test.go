package checkhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Body capture is the health check's opt-in: an http check must neither read
// nor keep a body no assertion asked for.
func TestBodyCaptureOffByDefault(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	info := &responseInfo{}
	checker := &HTTPChecker{}

	result, err := checker.executeRequest(context.Background(), &HTTPConfig{URL: server.URL}, info)
	require.NoError(t, err)
	require.Equal(t, checkerdef.StatusUp, result.Status)
	require.Nil(t, info.body, "no body is kept for an http check")
	require.Zero(t, info.statusCode)
	require.Empty(t, info.contentType)
	require.True(t, info.verdictReached)
}

func TestExecuteCapturingBodyKeepsBodyOnUnexpectedStatus(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/health+json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"fail"}`))
	}))
	defer server.Close()

	checker := &HTTPChecker{}

	result, resp, err := checker.ExecuteCapturingBody(context.Background(), &HTTPConfig{URL: server.URL})
	require.NoError(t, err)
	// The http verdict judges the status only: 503 is down...
	require.Equal(t, checkerdef.StatusDown, result.Status)
	// ...but the body is still there for the caller.
	require.True(t, resp.Reached)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Equal(t, "application/health+json", resp.ContentType)
	require.JSONEq(t, `{"status":"fail"}`, string(resp.Body))
}

func TestExecuteCapturingBodyNetworkFailureHasNoResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	checker := &HTTPChecker{}

	result, resp, err := checker.ExecuteCapturingBody(context.Background(), &HTTPConfig{URL: url})
	require.NoError(t, err)
	require.NotEqual(t, checkerdef.StatusUp, result.Status)
	require.False(t, resp.Reached)
	require.Nil(t, resp.Body)
}
