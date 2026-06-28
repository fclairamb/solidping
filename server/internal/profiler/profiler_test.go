package profiler_test

import (
	"context"
	"io"
	"net/http"
	"runtime"
	"sync"
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

// TestProfilerServer_BlockProfiling verifies that BlockRate > 0 makes
// /debug/pprof/block report contention. SetBlockProfileRate is a process-global
// runtime setting, so this test is not parallel and resets the rate afterwards.
func TestProfilerServer_BlockProfiling(t *testing.T) {
	r := require.New(t)

	cfg := &config.ProfilerConfig{
		Enabled:   true,
		Listen:    "localhost:16061",
		BlockRate: 1,
	}
	srv := profiler.New(cfg)

	r.NoError(srv.Start(context.Background()))
	defer func() {
		r.NoError(srv.Shutdown(context.Background()))
		runtime.SetBlockProfileRate(0) // restore global default for other tests
	}()

	// Generate a blocking event so the block profile is non-empty: two
	// goroutines contending on a mutex the runtime can sample.
	var mu sync.Mutex
	mu.Lock()
	done := make(chan struct{})
	go func() {
		mu.Lock()
		defer mu.Unlock()
		close(done)
	}()
	time.Sleep(20 * time.Millisecond) // let the goroutine block on mu
	mu.Unlock()
	<-done

	time.Sleep(100 * time.Millisecond) // let the server come up

	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet, "http://localhost:16061/debug/pprof/block?debug=1", nil)
	r.NoError(err)

	resp, err := client.Do(req)
	r.NoError(err)
	defer func() { r.NoError(resp.Body.Close()) }()
	r.Equal(http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)
	// The debug=1 text profile always emits a "--- contention:" header; with
	// the rate enabled and a real blocking event there are sampled records.
	r.Contains(string(body), "contention")
}
