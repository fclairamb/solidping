// Package middleware provides HTTP middleware for authentication and authorization.
package middleware

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bunrouter"
	"golang.org/x/time/rate"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// limitedPrefix scopes rate/concurrency limiting to API traffic. Static assets
// (dash, status0, embedded SPA files, /pub, /docs, /openapi.yaml) are served
// from embedded FS and aren't an abuse surface — including them caused first
// paint to 429 because a single page load fires more parallel asset requests
// than the per-IP concurrency cap allows.
const limitedPrefix = "/api/v1/"

// excludedPrefixes lists /api/v1/ sub-paths exempt from both limits.
// Workers and heartbeat have fundamentally different traffic patterns; /api/mgmt
// stays unlimited as a whole (outside limitedPrefix) so a rate-limited client
// can still call /api/mgmt/limits to discover how long until its bucket refills.
var excludedPrefixes = []string{ //nolint:gochecknoglobals // package-level constant list
	"/api/v1/workers/",
	"/api/v1/heartbeat/",
}

// Response headers set when a request was held in a waiting room before being
// admitted. Both report the wait time in integer milliseconds. They are only
// set on responses that ultimately succeeded — 429s set Retry-After instead.
const (
	HeaderRateLimitDelayedMs  = "X-Rate-Limit-Delayed-Ms"
	HeaderConcurrencyQueuedMs = "X-Concurrency-Queued-Ms"
)

// ipEntry holds per-IP rate-limiter and concurrency state.
//
// sem is the active-slot semaphore (capacity = MaxConcurrent).
// rateQueue and concurQueue are admission semaphores for the waiting rooms —
// the goroutine of each waiting request *is* its queue slot, so the channels
// only count occupancy, they never carry data.
type ipEntry struct {
	limiter     *rate.Limiter
	sem         chan struct{}
	rateQueue   chan struct{}
	concurQueue chan struct{}
	lastSeen    time.Time
}

// RateLimiter provides per-IP rate limiting and concurrency limiting middleware.
type RateLimiter struct {
	entries sync.Map
	cfg     config.RateLimitConfig
}

// NewRateLimiter creates a RateLimiter and starts its background cleanup goroutine.
//
//nolint:revive // ctx after cfg is intentional here
func NewRateLimiter(cfg config.RateLimitConfig, ctx context.Context) *RateLimiter {
	rl := &RateLimiter{cfg: cfg}
	go rl.cleanupLoop(ctx)
	return rl
}

func (rl *RateLimiter) getEntry(ip string) *ipEntry {
	if val, ok := rl.entries.Load(ip); ok {
		entry, ok2 := val.(*ipEntry)
		if ok2 {
			entry.lastSeen = time.Now()
			return entry
		}
	}

	var lim *rate.Limiter
	if rl.cfg.RequestsPerMinute > 0 {
		r := rate.Limit(float64(rl.cfg.RequestsPerMinute) / 60.0)
		lim = rate.NewLimiter(r, rl.cfg.Burst)
	}

	var sem chan struct{}
	if rl.cfg.MaxConcurrent > 0 {
		sem = make(chan struct{}, rl.cfg.MaxConcurrent)
	}

	var rateQueue chan struct{}
	if rl.cfg.RequestsPerMinute > 0 && rl.cfg.RateQueue > 0 {
		rateQueue = make(chan struct{}, rl.cfg.RateQueue)
	}

	var concurQueue chan struct{}
	if rl.cfg.MaxConcurrent > 0 && rl.cfg.ConcurrencyQueue > 0 {
		concurQueue = make(chan struct{}, rl.cfg.ConcurrencyQueue)
	}

	entry := &ipEntry{
		limiter:     lim,
		sem:         sem,
		rateQueue:   rateQueue,
		concurQueue: concurQueue,
		lastSeen:    time.Now(),
	}
	actual, _ := rl.entries.LoadOrStore(ip, entry)
	if typedEntry, ok := actual.(*ipEntry); ok {
		return typedEntry
	}
	return entry
}

// cleanupLoop evicts entries idle for more than 5 minutes, running every 2 minutes.
func (rl *RateLimiter) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-5 * time.Minute)
			rl.entries.Range(func(k, val any) bool {
				if entry, ok := val.(*ipEntry); ok && entry.lastSeen.Before(cutoff) {
					rl.entries.Delete(k)
				}
				return true
			})
		}
	}
}

func isExcluded(path string) bool {
	if !strings.HasPrefix(path, limitedPrefix) {
		return true
	}
	for _, prefix := range excludedPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// extractIPFromXFF parses X-Forwarded-For and returns the real client IP
// by stripping the last trustedProxies entries (the trusted proxy hops).
func extractIPFromXFF(xff string, trustedProxies int) string {
	parts := strings.Split(xff, ",")
	idx := len(parts) - trustedProxies
	if idx > 0 {
		return strings.TrimSpace(parts[idx-1])
	}
	return ""
}

// extractIP returns the client IP, respecting the configured TrustedProxies count.
func extractIP(req *http.Request, trustedProxies int) string {
	if trustedProxies > 0 {
		if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
			if ip := extractIPFromXFF(xff, trustedProxies); ip != "" {
				return ip
			}
		}
		if xri := req.Header.Get("X-Real-IP"); xri != "" {
			return xri
		}
	}

	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}
	return host
}

func writeLimitError(writer http.ResponseWriter, code base.ErrorCode, title, detail string) {
	writer.Header().Set("Content-Type", "application/json")
	if code == base.ErrorCodeRateLimited {
		writer.Header().Set("Retry-After", "60")
	}
	writer.WriteHeader(http.StatusTooManyRequests)
	body, err := json.Marshal(base.ErrorResponse{
		Title:  title,
		Code:   string(code),
		Detail: detail,
	})
	if err == nil {
		_, _ = writer.Write(body)
	}
}

func rejectRate(writer http.ResponseWriter) {
	prommetrics.HTTPRateLimited.WithLabelValues("rate").Inc()
	writeLimitError(writer, base.ErrorCodeRateLimited,
		"Too many requests",
		"Rate limit exceeded, please slow down",
	)
}

func rejectConcurrency(writer http.ResponseWriter) {
	prommetrics.HTTPRateLimited.WithLabelValues("concurrency").Inc()
	writeLimitError(writer, base.ErrorCodeConcurrencyLimited,
		"Too many concurrent requests",
		"Too many simultaneous requests from your IP",
	)
}

// queueWaitContext derives a context that fires after MaxQueueWait, or returns
// the request's own context unchanged when the ceiling is disabled.
func (rl *RateLimiter) queueWaitContext(req bunrouter.Request) (context.Context, context.CancelFunc) {
	if rl.cfg.MaxQueueWait <= 0 {
		return req.Context(), func() {}
	}
	return context.WithTimeout(req.Context(), rl.cfg.MaxQueueWait)
}

// RateLimit is a bunrouter middleware that enforces the per-IP token-bucket
// rate limit with a bounded slow-lane queue: a request that loses the
// fast-path token race may wait for the next refill, up to RateQueue
// requests deep and MaxQueueWait long, before being rejected with 429.
func (rl *RateLimiter) RateLimit(next bunrouter.HandlerFunc) bunrouter.HandlerFunc {
	return func(writer http.ResponseWriter, req bunrouter.Request) error {
		if rl.cfg.RequestsPerMinute == 0 || isExcluded(req.URL.Path) {
			return next(writer, req)
		}

		ip := extractIP(req.Request, rl.cfg.TrustedProxies)
		entry := rl.getEntry(ip)

		if entry.limiter.Allow() {
			return next(writer, req)
		}

		if entry.rateQueue == nil {
			rejectRate(writer)
			return nil
		}

		select {
		case entry.rateQueue <- struct{}{}:
		default:
			rejectRate(writer)
			return nil
		}
		defer func() { <-entry.rateQueue }()

		reservation := entry.limiter.Reserve()
		if !reservation.OK() {
			rejectRate(writer)
			return nil
		}
		wait := reservation.Delay()
		if rl.cfg.MaxQueueWait > 0 && wait > rl.cfg.MaxQueueWait {
			reservation.Cancel()
			rejectRate(writer)
			return nil
		}

		ctx, cancel := rl.queueWaitContext(req)
		defer cancel()

		timer := time.NewTimer(wait)
		defer timer.Stop()

		select {
		case <-timer.C:
			prommetrics.HTTPRateLimited.WithLabelValues("rate_delayed").Inc()
			writer.Header().Set(HeaderRateLimitDelayedMs, strconv.FormatInt(wait.Milliseconds(), 10))
			return next(writer, req)
		case <-ctx.Done():
			reservation.Cancel()
			rejectRate(writer)
			return nil
		}
	}
}

// IPState is a read-only snapshot of a single IP's rate-limit and concurrency
// state, returned by StateFor for the /api/mgmt/limits introspection endpoint.
type IPState struct {
	// Remaining is the number of rate-limit tokens currently available for
	// the IP (floored at 0). Equals Burst for an IP with no entry yet.
	Remaining float64
	// InFlight is the number of concurrent requests the IP currently holds.
	// Zero for an IP with no entry yet.
	InFlight int
}

// Config returns the limiter's configuration. Used by the introspection
// handler to report the configured limits without re-reading config.
func (rl *RateLimiter) Config() config.RateLimitConfig {
	return rl.cfg
}

// EntryCount returns the number of per-IP entries currently held in the
// limiter's map. The map grows O(unique IPs) and is pruned by cleanupLoop;
// this count lets the memory surface verify cleanup keeps pace under a wide
// client base or scan. O(n) range; only called at metrics scrape time.
func (rl *RateLimiter) EntryCount() int {
	count := 0
	rl.entries.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// ExtractIP returns the client IP for the request using the configured
// TrustedProxies count. Exposed so handlers (e.g. /api/mgmt/limits) can use
// the same logic as the middlewares.
func (rl *RateLimiter) ExtractIP(req *http.Request) string {
	return extractIP(req, rl.cfg.TrustedProxies)
}

// StateFor returns a snapshot of the per-IP rate-limit and concurrency state
// without consuming a token or acquiring a semaphore slot. If the IP has no
// entry yet, it returns the "fresh IP" defaults (full burst, zero in-flight).
func (rl *RateLimiter) StateFor(ip string) IPState {
	state := IPState{
		Remaining: float64(rl.cfg.Burst),
		InFlight:  0,
	}

	val, ok := rl.entries.Load(ip)
	if !ok {
		return state
	}
	entry, ok := val.(*ipEntry)
	if !ok {
		return state
	}

	if entry.limiter != nil {
		tokens := entry.limiter.Tokens()
		if tokens < 0 {
			tokens = 0
		}
		state.Remaining = tokens
	}
	if entry.sem != nil {
		state.InFlight = len(entry.sem)
	}
	return state
}

// ConcurrencyLimit is a bunrouter middleware that enforces the per-IP
// concurrency limit with a bounded waiting room: a request that finds the
// semaphore full may wait for an active slot to free, up to
// ConcurrencyQueue requests deep and MaxQueueWait long, before being
// rejected with 429.
func (rl *RateLimiter) ConcurrencyLimit(next bunrouter.HandlerFunc) bunrouter.HandlerFunc {
	return func(writer http.ResponseWriter, req bunrouter.Request) error {
		if rl.cfg.MaxConcurrent == 0 || isExcluded(req.URL.Path) {
			return next(writer, req)
		}

		ip := extractIP(req.Request, rl.cfg.TrustedProxies)
		entry := rl.getEntry(ip)

		select {
		case entry.sem <- struct{}{}:
			defer func() { <-entry.sem }()
			return next(writer, req)
		default:
		}

		if entry.concurQueue == nil {
			rejectConcurrency(writer)
			return nil
		}

		select {
		case entry.concurQueue <- struct{}{}:
		default:
			rejectConcurrency(writer)
			return nil
		}
		defer func() { <-entry.concurQueue }()

		ctx, cancel := rl.queueWaitContext(req)
		defer cancel()

		start := time.Now()
		select {
		case entry.sem <- struct{}{}:
			defer func() { <-entry.sem }()
			waited := time.Since(start)
			prommetrics.HTTPRateLimited.WithLabelValues("concurrency_queued").Inc()
			writer.Header().Set(HeaderConcurrencyQueuedMs, strconv.FormatInt(waited.Milliseconds(), 10))
			return next(writer, req)
		case <-ctx.Done():
			rejectConcurrency(writer)
			return nil
		}
	}
}
