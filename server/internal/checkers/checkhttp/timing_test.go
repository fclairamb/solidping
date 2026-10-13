package checkhttp

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func metricFloat(t *testing.T, result *checkerdef.Result, key string) float64 {
	t.Helper()

	value, ok := result.Metrics[key]
	require.Truef(t, ok, "metric %s missing from %v", key, result.Metrics)

	f, ok := value.(float64)
	require.True(t, ok)
	require.GreaterOrEqual(t, f, 0.0)

	return f
}

func okServerHandler(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("hello"))
}

func requireSumWithinDuration(t *testing.T, result *checkerdef.Result) {
	t.Helper()

	sum := metricFloat(t, result, "dns_time_ms") + metricFloat(t, result, "connect_time_ms") +
		metricFloat(t, result, "ttfb_time_ms")
	if _, ok := result.Metrics["tls_time_ms"]; ok {
		sum += metricFloat(t, result, "tls_time_ms")
	}

	require.LessOrEqual(t, sum, float64(result.Duration.Microseconds())/1000.0)
}

func localhostURL(t *testing.T, serverURL string) string {
	t.Helper()

	idx := strings.LastIndex(serverURL, ":")
	require.Positive(t, idx)

	return "localhost" + serverURL[idx:]
}

func TestHTTPTimingPlain(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(okServerHandler))
	defer server.Close()

	result, err := (&HTTPChecker{}).Execute(t.Context(), &HTTPConfig{
		URL: "http://" + localhostURL(t, server.URL), Method: http.MethodGet,
	})
	require.NoError(t, err)
	require.Equal(t, checkerdef.StatusUp, result.Status)

	metricFloat(t, result, "dns_time_ms")
	metricFloat(t, result, "connect_time_ms")
	metricFloat(t, result, "ttfb_time_ms")
	require.NotContains(t, result.Metrics, "tls_time_ms")
	require.NotContains(t, result.Metrics, "download_time_ms")
	require.NotContains(t, result.Metrics, "redirect_time_ms")
	requireSumWithinDuration(t, result)
}

func TestHTTPTimingTLS(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(okServerHandler))
	defer server.Close()

	verify := false
	result, err := (&HTTPChecker{}).Execute(t.Context(), &HTTPConfig{
		URL: "https://" + localhostURL(t, server.URL), Method: http.MethodGet, VerifySsl: &verify,
	})
	require.NoError(t, err)
	require.Equal(t, checkerdef.StatusUp, result.Status)

	metricFloat(t, result, "tls_time_ms")
	requireSumWithinDuration(t, result)
}

func TestHTTPTimingDownloadOnlyWhenBodyRead(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(okServerHandler))
	defer server.Close()

	withBody, err := (&HTTPChecker{}).Execute(t.Context(), &HTTPConfig{
		URL: server.URL, Method: http.MethodGet, BodyExpect: "hello",
	})
	require.NoError(t, err)
	require.Equal(t, checkerdef.StatusUp, withBody.Status)
	metricFloat(t, withBody, "download_time_ms")

	without, err := (&HTTPChecker{}).Execute(t.Context(), &HTTPConfig{URL: server.URL, Method: http.MethodGet})
	require.NoError(t, err)
	require.NotContains(t, without.Metrics, "download_time_ms")
}

func TestHTTPTimingRedirectUsesFinalHop(t *testing.T) {
	t.Parallel()

	const delay = 50 * time.Millisecond

	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(delay)
		_, _ = w.Write([]byte("b"))
	}))
	defer serverB.Close()

	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, serverB.URL, http.StatusFound)
	}))
	defer serverA.Close()

	result, err := (&HTTPChecker{}).Execute(t.Context(), &HTTPConfig{URL: serverA.URL, Method: http.MethodGet})
	require.NoError(t, err)
	require.Equal(t, checkerdef.StatusUp, result.Status)

	metricFloat(t, result, "redirect_time_ms")
	require.GreaterOrEqual(t, metricFloat(t, result, "ttfb_time_ms"), float64(delay.Milliseconds()))
	metricFloat(t, result, "connect_time_ms")
}

func TestHTTPTimingFreshConnectionEveryProbe(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(okServerHandler))
	defer server.Close()

	checker := &HTTPChecker{}
	cfg := &HTTPConfig{URL: server.URL, Method: http.MethodGet}

	for range 2 {
		result, err := checker.Execute(t.Context(), cfg)
		require.NoError(t, err)
		metricFloat(t, result, "connect_time_ms")
	}
}

func TestHTTPTimingConnectionRefused(t *testing.T) {
	t.Parallel()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := listener.Addr().String()
	require.NoError(t, listener.Close())

	result, err := (&HTTPChecker{}).Execute(t.Context(), &HTTPConfig{URL: "http://" + addr, Method: http.MethodGet})
	require.NoError(t, err)
	require.Equal(t, checkerdef.StatusDown, result.Status)

	for _, key := range []string{
		"dns_time_ms", "connect_time_ms", "tls_time_ms", "ttfb_time_ms", "download_time_ms",
	} {
		require.NotContains(t, result.Metrics, key)
	}
}

func TestHTTPTimingTimeoutAfterConnect(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}))

	defer server.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	result, err := (&HTTPChecker{}).Execute(ctx, &HTTPConfig{URL: server.URL, Method: http.MethodGet})
	require.NoError(t, err)
	require.NotEqual(t, checkerdef.StatusUp, result.Status)

	metricFloat(t, result, "connect_time_ms")
	require.NotContains(t, result.Metrics, "ttfb_time_ms")
}
