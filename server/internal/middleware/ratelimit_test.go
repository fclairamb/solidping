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
		"/api/mgmt/limits",
		"/api/mgmt/version",
		"/metrics",
		"/api/v1/workers/heartbeat",
		"/api/v1/heartbeat/org/identifier",
		"/dash0/",
		"/dash0/assets/index-abc.js",
		"/status0/",
		"/",
		"/index.html",
		"/openapi.yaml",
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

	// inside is signaled once the handler has acquired the semaphore.
	// release lets the holders exit after we've confirmed the overflow 429.
	inside := make(chan struct{}, 2)
	release := make(chan struct{})
	blockingHandler := func(w http.ResponseWriter, _ bunrouter.Request) error {
		inside <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
		return nil
	}
	handler := rl.ConcurrencyLimit(blockingHandler)

	var holders sync.WaitGroup
	for range 2 {
		holders.Add(1)
		go func() {
			defer holders.Done()
			w := httptest.NewRecorder()
			_ = handler(w, newBunRequest("2.2.2.2", "/api/v1/orgs/default/checks"))
		}()
	}
	// Wait until both holders have actually acquired the semaphore.
	<-inside
	<-inside

	// Now the third request must be rejected immediately with 429.
	w := httptest.NewRecorder()
	_ = handler(w, newBunRequest("2.2.2.2", "/api/v1/orgs/default/checks"))
	r.Equal(http.StatusTooManyRequests, w.Code, "third request should be rejected when sem is full")

	close(release)
	holders.Wait()
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

func TestStateFor_FreshIPReturnsBurst(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := config.RateLimitConfig{
		RequestsPerMinute: 300,
		Burst:             60,
		MaxConcurrent:     20,
	}
	rl := middleware.NewRateLimiter(cfg, context.Background())

	state := rl.StateFor("6.6.6.6")
	r.InDelta(60.0, state.Remaining, 0.0001, "fresh IP should have a full burst available")
	r.Zero(state.InFlight, "fresh IP should report no in-flight")
}

func TestStateFor_ReflectsTokenConsumption(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := config.RateLimitConfig{
		RequestsPerMinute: 60,
		Burst:             5,
	}
	rl := middleware.NewRateLimiter(cfg, context.Background())
	handler := rl.RateLimit(okHandler())

	for range 3 {
		w := httptest.NewRecorder()
		_ = handler(w, newBunRequest("7.7.7.7", "/api/v1/orgs/default/checks"))
		r.Equal(http.StatusOK, w.Code)
	}

	state := rl.StateFor("7.7.7.7")
	// 3 of 5 burst consumed; refill at 1/s is small over a test runtime.
	r.LessOrEqual(state.Remaining, 2.5, "remaining tokens should reflect consumption")
}

func TestStateFor_ReflectsInFlight(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := config.RateLimitConfig{
		RequestsPerMinute: 0,
		MaxConcurrent:     3,
	}
	rl := middleware.NewRateLimiter(cfg, context.Background())

	inside := make(chan struct{}, 2)
	release := make(chan struct{})
	blockingHandler := func(w http.ResponseWriter, _ bunrouter.Request) error {
		inside <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
		return nil
	}
	handler := rl.ConcurrencyLimit(blockingHandler)

	var holders sync.WaitGroup
	for range 2 {
		holders.Add(1)
		go func() {
			defer holders.Done()
			w := httptest.NewRecorder()
			_ = handler(w, newBunRequest("8.8.8.8", "/api/v1/orgs/default/checks"))
		}()
	}
	<-inside
	<-inside

	state := rl.StateFor("8.8.8.8")
	r.Equal(2, state.InFlight, "two requests holding slots should show up")

	close(release)
	holders.Wait()
}
