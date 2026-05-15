package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

func okHandler() bunrouter.HandlerFunc {
	return func(w http.ResponseWriter, _ bunrouter.Request) error {
		w.WriteHeader(http.StatusOK)
		return nil
	}
}

func newBunRequest(ip, path string) bunrouter.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
	r.RemoteAddr = ip + ":12345"
	return bunrouter.NewRequest(r)
}

func TestRateLimit_BlocksAfterLimit(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := config.RateLimitConfig{
		RequestsPerMinute: 5,
		Burst:             5,
		MaxConcurrent:     0,
	}
	rl := middleware.NewRateLimiter(cfg, context.Background())
	handler := rl.RateLimit(okHandler())

	var lastStatus int
	for i := range 7 {
		w := httptest.NewRecorder()
		_ = handler(w, newBunRequest("1.2.3.4", "/api/v1/orgs/default/checks"))
		lastStatus = w.Code
		if i < 5 {
			r.Equal(http.StatusOK, w.Code, "request %d should pass", i)
		}
	}
	r.Equal(http.StatusTooManyRequests, lastStatus, "request after limit should be rejected")
}

func TestRateLimit_ExcludedPaths(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := config.RateLimitConfig{
		RequestsPerMinute: 1,
		Burst:             1,
		MaxConcurrent:     0,
	}
	rl := middleware.NewRateLimiter(cfg, context.Background())
	handler := rl.RateLimit(okHandler())

	// Exhaust limit on a normal path first.
	for range 3 {
		w := httptest.NewRecorder()
		_ = handler(w, newBunRequest("1.2.3.4", "/api/v1/orgs/default/checks"))
	}

	excludedPaths := []string{
		"/api/mgmt/health",
		"/metrics",
		"/api/v1/workers/heartbeat",
		"/api/v1/heartbeat/org/identifier",
	}
	for _, path := range excludedPaths {
		w := httptest.NewRecorder()
		_ = handler(w, newBunRequest("1.2.3.4", path))
		r.Equal(http.StatusOK, w.Code, "excluded path %s must not be limited", path)
	}
}

func TestConcurrencyLimit_BlocksWhenFull(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := config.RateLimitConfig{
		RequestsPerMinute: 0,
		MaxConcurrent:     2,
	}
	rl := middleware.NewRateLimiter(cfg, context.Background())

	// A handler that blocks until released.
	release := make(chan struct{})
	blockingHandler := func(w http.ResponseWriter, _ bunrouter.Request) error {
		<-release
		w.WriteHeader(http.StatusOK)
		return nil
	}
	handler := rl.ConcurrencyLimit(blockingHandler)

	var wg sync.WaitGroup
	results := make([]int, 3)
	ready := make(chan struct{}, 3)

	for i := range 3 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			ready <- struct{}{}
			_ = handler(w, newBunRequest("2.2.2.2", "/api/v1/orgs/default/checks"))
			results[idx] = w.Code
		}(i)
	}

	// Wait for all goroutines to start.
	for range 3 {
		<-ready
	}
	// Unblock the blocking ones.
	close(release)
	wg.Wait()

	// At least one should be 429 (the overflow request).
	has429 := false
	for _, code := range results {
		if code == http.StatusTooManyRequests {
			has429 = true
		}
	}
	r.True(has429, "at least one request should be rejected with 429 when concurrency limit is full")
}

func TestConcurrencyLimit_ExcludedPaths(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := config.RateLimitConfig{
		RequestsPerMinute: 0,
		MaxConcurrent:     1,
	}
	rl := middleware.NewRateLimiter(cfg, context.Background())

	// Block the one slot.
	release := make(chan struct{})
	blockingHandler := func(w http.ResponseWriter, req bunrouter.Request) error {
		// Only block on the non-excluded path.
		if req.URL.Path == "/api/v1/orgs/default/checks" {
			<-release
		}
		w.WriteHeader(http.StatusOK)
		return nil
	}
	handler := rl.ConcurrencyLimit(blockingHandler)

	// Fill the slot.
	started := make(chan struct{})
	go func() {
		close(started)
		w := httptest.NewRecorder()
		_ = handler(w, newBunRequest("3.3.3.3", "/api/v1/orgs/default/checks"))
	}()
	<-started

	// Health check must pass even though the slot is occupied.
	w := httptest.NewRecorder()
	_ = handler(w, newBunRequest("3.3.3.3", "/api/mgmt/health"))
	r.Equal(http.StatusOK, w.Code, "/api/mgmt/health must not be concurrency-limited")

	close(release)
}

func TestExtractIP_TrustedProxies(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// With TrustedProxies=1 the middleware should read X-Forwarded-For.
	cfg := config.RateLimitConfig{
		RequestsPerMinute: 5,
		Burst:             5,
		TrustedProxies:    1,
	}
	rl := middleware.NewRateLimiter(cfg, context.Background())
	handler := rl.RateLimit(okHandler())

	// Requests from different XFF IPs but same RemoteAddr should each have their own bucket.
	for _, xff := range []string{"10.0.0.1", "10.0.0.2"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(
			context.Background(), http.MethodGet, "/api/v1/orgs/default/checks", http.NoBody,
		)
		req.RemoteAddr = "192.168.1.1:12345"
		req.Header.Set("X-Forwarded-For", xff)
		_ = handler(w, bunrouter.NewRequest(req))
		r.Equal(http.StatusOK, w.Code, "request from %s should pass", xff)
	}
}

func TestRateLimit_Disabled(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := config.RateLimitConfig{
		RequestsPerMinute: 0, // disabled
		MaxConcurrent:     0, // disabled
	}
	rl := middleware.NewRateLimiter(cfg, context.Background())
	handler := rl.RateLimit(okHandler())

	for range 100 {
		w := httptest.NewRecorder()
		_ = handler(w, newBunRequest("4.4.4.4", "/api/v1/orgs/default/checks"))
		r.Equal(http.StatusOK, w.Code)
	}
}

func TestRateLimit_RetryAfterHeader(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := config.RateLimitConfig{
		RequestsPerMinute: 1,
		Burst:             1,
		MaxConcurrent:     0,
	}
	rl := middleware.NewRateLimiter(cfg, context.Background())
	handler := rl.RateLimit(okHandler())

	// Exhaust the limit.
	for range 3 {
		w := httptest.NewRecorder()
		_ = handler(w, newBunRequest("5.5.5.5", "/api/v1/orgs/default/checks"))
		if w.Code == http.StatusTooManyRequests {
			r.Equal("60", w.Header().Get("Retry-After"))
			return
		}
	}
	t.Fatal("expected a 429 response")
}
