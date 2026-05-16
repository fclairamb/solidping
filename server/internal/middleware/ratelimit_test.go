package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// newBunRequestCtx is like newBunRequest but allows passing a parent context.
func newBunRequestCtx(ctx context.Context, ip, path string) bunrouter.Request {
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, path, http.NoBody)
	r.RemoteAddr = ip + ":12345"
	return bunrouter.NewRequest(r)
}

func TestRateLimit_QueuesUntilRefill(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// 60 rpm = 1 token per second. Burst 2, queue 3.
	cfg := config.RateLimitConfig{
		RequestsPerMinute: 60,
		Burst:             2,
		RateQueue:         3,
		MaxQueueWait:      5 * time.Second,
	}
	rl := middleware.NewRateLimiter(cfg, context.Background())
	handler := rl.RateLimit(okHandler())

	// Burn the burst.
	for range 2 {
		w := httptest.NewRecorder()
		_ = handler(w, newBunRequest("10.0.0.1", "/api/v1/orgs/default/checks"))
		r.Equal(http.StatusOK, w.Code)
	}

	// Fire 3 in parallel; all should pass after waiting (delayed header set).
	var wg sync.WaitGroup
	var passed atomic.Int32
	var maxDelay atomic.Int64
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			_ = handler(w, newBunRequest("10.0.0.1", "/api/v1/orgs/default/checks"))
			if w.Code == http.StatusOK {
				passed.Add(1)
				if hdr := w.Header().Get(middleware.HeaderRateLimitDelayedMs); hdr != "" {
					if n, err := time.ParseDuration(hdr + "ms"); err == nil && n.Milliseconds() > maxDelay.Load() {
						maxDelay.Store(n.Milliseconds())
					}
				}
			}
		}()
	}
	wg.Wait()

	r.Equal(int32(3), passed.Load(), "all 3 queued requests should pass")
	r.Positive(maxDelay.Load(), "at least one request should report a non-zero delay")
}

func TestRateLimit_RejectsBeyondQueue(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Slow refill so we don't get tokens during the test.
	cfg := config.RateLimitConfig{
		RequestsPerMinute: 6, // 1 per 10s
		Burst:             1,
		RateQueue:         2,
		MaxQueueWait:      100 * time.Millisecond,
	}
	rl := middleware.NewRateLimiter(cfg, context.Background())
	handler := rl.RateLimit(okHandler())

	// Consume the burst.
	w0 := httptest.NewRecorder()
	_ = handler(w0, newBunRequest("10.0.0.2", "/api/v1/orgs/default/checks"))
	r.Equal(http.StatusOK, w0.Code)

	// Fire RateQueue + 1 = 3 in parallel. Two get queued (then 429 by ceiling);
	// the third can't even claim a queue slot and gets immediate 429.
	var wg sync.WaitGroup
	var ok429 atomic.Int32
	var sawRetryAfter atomic.Int32
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			_ = handler(w, newBunRequest("10.0.0.2", "/api/v1/orgs/default/checks"))
			if w.Code == http.StatusTooManyRequests {
				ok429.Add(1)
				if w.Header().Get("Retry-After") == "60" {
					sawRetryAfter.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	r.Equal(int32(3), ok429.Load(), "all 3 should be rejected with 429")
	r.Equal(int32(3), sawRetryAfter.Load(), "all rejections should carry Retry-After: 60")
}

func TestRateLimit_HonorsContextCancel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := config.RateLimitConfig{
		RequestsPerMinute: 6,
		Burst:             1,
		RateQueue:         3,
		MaxQueueWait:      5 * time.Second,
	}
	rl := middleware.NewRateLimiter(cfg, context.Background())
	handler := rl.RateLimit(okHandler())

	// Consume the burst.
	w0 := httptest.NewRecorder()
	_ = handler(w0, newBunRequest("10.0.0.3", "/api/v1/orgs/default/checks"))
	r.Equal(http.StatusOK, w0.Code)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		_ = handler(w, newBunRequestCtx(ctx, "10.0.0.3", "/api/v1/orgs/default/checks"))
		done <- w.Code
	}()
	// Give the request time to claim a queue slot.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case code := <-done:
		r.Equal(http.StatusTooManyRequests, code)
	case <-time.After(2 * time.Second):
		t.Fatal("cancel should have aborted the wait")
	}
}

func TestRateLimit_HonorsMaxQueueWait(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Refill needs ~10s but ceiling is 50ms — should bail out fast.
	cfg := config.RateLimitConfig{
		RequestsPerMinute: 6,
		Burst:             1,
		RateQueue:         3,
		MaxQueueWait:      50 * time.Millisecond,
	}
	rl := middleware.NewRateLimiter(cfg, context.Background())
	handler := rl.RateLimit(okHandler())

	// Consume the burst.
	w0 := httptest.NewRecorder()
	_ = handler(w0, newBunRequest("10.0.0.4", "/api/v1/orgs/default/checks"))
	r.Equal(http.StatusOK, w0.Code)

	start := time.Now()
	w := httptest.NewRecorder()
	_ = handler(w, newBunRequest("10.0.0.4", "/api/v1/orgs/default/checks"))
	elapsed := time.Since(start)
	r.Equal(http.StatusTooManyRequests, w.Code)
	r.Less(elapsed, 500*time.Millisecond, "should not wait longer than MaxQueueWait")
}

func TestConcurrencyLimit_QueuesUntilRelease(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := config.RateLimitConfig{
		MaxConcurrent:    2,
		ConcurrencyQueue: 2,
		MaxQueueWait:     5 * time.Second,
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
	fastHandler := rl.ConcurrencyLimit(okHandler())

	var holders sync.WaitGroup
	for range 2 {
		holders.Add(1)
		go func() {
			defer holders.Done()
			w := httptest.NewRecorder()
			_ = handler(w, newBunRequest("20.0.0.1", "/api/v1/orgs/default/checks"))
		}()
	}
	<-inside
	<-inside

	// Two queued waiters using fastHandler — they pass as soon as a slot frees.
	var queued sync.WaitGroup
	var queuedOK atomic.Int32
	var sawHeader atomic.Int32
	for range 2 {
		queued.Add(1)
		go func() {
			defer queued.Done()
			w := httptest.NewRecorder()
			_ = fastHandler(w, newBunRequest("20.0.0.1", "/api/v1/orgs/default/checks"))
			if w.Code == http.StatusOK {
				queuedOK.Add(1)
				if w.Header().Get(middleware.HeaderConcurrencyQueuedMs) != "" {
					sawHeader.Add(1)
				}
			}
		}()
	}

	// Let the queued requests register; then release the holders.
	time.Sleep(50 * time.Millisecond)
	close(release)
	holders.Wait()
	queued.Wait()

	r.Equal(int32(2), queuedOK.Load(), "both queued requests should succeed")
	r.Equal(int32(2), sawHeader.Load(), "both should carry the queued-ms header")
}

func TestConcurrencyLimit_RejectsBeyondQueue(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := config.RateLimitConfig{
		MaxConcurrent:    2,
		ConcurrencyQueue: 2,
		MaxQueueWait:     50 * time.Millisecond,
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
			_ = handler(w, newBunRequest("20.0.0.2", "/api/v1/orgs/default/checks"))
		}()
	}
	<-inside
	<-inside

	// Fire 3 more: 2 will wait (and time out → 429), 1 won't even get a queue slot.
	var wg sync.WaitGroup
	var ok429 atomic.Int32
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			_ = handler(w, newBunRequest("20.0.0.2", "/api/v1/orgs/default/checks"))
			if w.Code == http.StatusTooManyRequests {
				ok429.Add(1)
			}
		}()
	}
	wg.Wait()
	r.Equal(int32(3), ok429.Load(), "all 3 overflow requests should be rejected")

	close(release)
	holders.Wait()
}

func TestConcurrencyLimit_HonorsContextCancel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := config.RateLimitConfig{
		MaxConcurrent:    1,
		ConcurrencyQueue: 2,
		MaxQueueWait:     5 * time.Second,
	}
	rl := middleware.NewRateLimiter(cfg, context.Background())

	release := make(chan struct{})
	inside := make(chan struct{}, 1)
	blockingHandler := func(w http.ResponseWriter, _ bunrouter.Request) error {
		inside <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
		return nil
	}
	handler := rl.ConcurrencyLimit(blockingHandler)

	// Take the one slot.
	var holder sync.WaitGroup
	holder.Add(1)
	go func() {
		defer holder.Done()
		w := httptest.NewRecorder()
		_ = handler(w, newBunRequest("20.0.0.3", "/api/v1/orgs/default/checks"))
	}()
	<-inside

	// Start a queued request; cancel its context.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		_ = rl.ConcurrencyLimit(okHandler())(w, newBunRequestCtx(ctx, "20.0.0.3", "/api/v1/orgs/default/checks"))
		done <- w.Code
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case code := <-done:
		r.Equal(http.StatusTooManyRequests, code)
	case <-time.After(2 * time.Second):
		t.Fatal("cancel should have aborted the wait")
	}

	close(release)
	holder.Wait()
}
