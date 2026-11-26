package checkdocker

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/egress"
)

func fakeDaemon(t *testing.T, handler http.HandlerFunc) *DockerConfig {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return &DockerConfig{Host: "tcp://" + strings.TrimPrefix(srv.URL, "http://"), Timeout: 500 * time.Millisecond}
}

func TestDaemonMode(t *testing.T) {
	t.Parallel()

	ctx := egress.WithGuard(t.Context(), egress.New(true))

	t.Run("up when the daemon answers", func(t *testing.T) {
		t.Parallel()

		cfg := fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Api-Version", "1.45")
			w.Header().Set("Ostype", "linux")
			_, _ = w.Write([]byte("OK"))
		})

		result, err := (&DockerChecker{}).Execute(ctx, cfg)
		require.NoError(t, err)
		require.Equal(t, checkerdef.StatusUp, result.Status, "%v", result.Output)
		require.Equal(t, "1.45", result.Output["apiVersion"])
		require.Equal(t, "linux", result.Output["osType"])
		require.Contains(t, result.Metrics, "ping_time_ms")
	})

	t.Run("down on a 500", func(t *testing.T) {
		t.Parallel()

		cfg := fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		})

		result, err := (&DockerChecker{}).Execute(ctx, cfg)
		require.NoError(t, err)
		require.Equal(t, checkerdef.StatusDown, result.Status, "%v", result.Output)
	})

	t.Run("down when the listener is closed", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.NotFoundHandler())
		addr := strings.TrimPrefix(srv.URL, "http://")
		srv.Close()

		result, err := (&DockerChecker{}).Execute(ctx, &DockerConfig{Host: "tcp://" + addr, Timeout: time.Second})
		require.NoError(t, err)
		require.Equal(t, checkerdef.StatusDown, result.Status, "%v", result.Output)
	})

	t.Run("timeout when the daemon hangs", func(t *testing.T) {
		t.Parallel()

		release := make(chan struct{})
		t.Cleanup(func() { close(release) })

		cfg := fakeDaemon(t, func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		})
		cfg.Timeout = 100 * time.Millisecond

		result, err := (&DockerChecker{}).Execute(ctx, cfg)
		require.NoError(t, err)
		require.Equal(t, checkerdef.StatusTimeout, result.Status, "%v", result.Output)
	})

	t.Run("keeps the egress rules", func(t *testing.T) {
		t.Parallel()

		cfg := fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("OK")) })

		denyCtx, rec := egress.WithRecorder(egress.WithGuard(t.Context(), egress.New(false)))
		_, err := (&DockerChecker{}).Execute(denyCtx, cfg)
		require.NoError(t, err)
		require.NotNil(t, rec.Denied())

		sockCtx, sockRec := egress.WithRecorder(egress.WithGuard(t.Context(), egress.New(false)))
		_, err = (&DockerChecker{}).Execute(sockCtx, &DockerConfig{Host: "unix:///nope-daemon.sock"})
		require.NoError(t, err)
		require.NotNil(t, sockRec.Denied())
	})
}
