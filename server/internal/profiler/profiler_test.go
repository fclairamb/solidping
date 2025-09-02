package profiler_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/profiler"
)

func TestProfilerServer_Disabled(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &config.ProfilerConfig{Enabled: false}
	srv := profiler.New(cfg)

	err := srv.Start(context.Background())
	r.NoError(err)

	// Should be safe to shutdown even when not started
	r.NoError(srv.Shutdown(context.Background()))
}

func TestProfilerServer_Enabled(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cfg := &config.ProfilerConfig{
		Enabled: true,
		Listen:  "localhost:16060",
	}
	srv := profiler.New(cfg)

	err := srv.Start(context.Background())
	r.NoError(err)

	defer func() {
		r.NoError(srv.Shutdown(context.Background()))
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	client := &http.Client{Timeout: 2 * time.Second}

	// Health check should be accessible
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost:16060/health", nil)
	r.NoError(err)

	resp, err := client.Do(req)
	r.NoError(err)
	r.NoError(resp.Body.Close())
	r.Equal(http.StatusOK, resp.StatusCode)

	// pprof index should be accessible
	pprofURL := "http://localhost:16060/debug/pprof/"
	req2, err := http.NewRequestWithContext(context.Background(), http.MethodGet, pprofURL, nil)
	r.NoError(err)

	resp2, err := client.Do(req2)
	r.NoError(err)
	r.NoError(resp2.Body.Close())
	r.Equal(http.StatusOK, resp2.StatusCode)
}
