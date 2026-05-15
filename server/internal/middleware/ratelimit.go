// Package middleware provides HTTP middleware for authentication and authorization.
package middleware

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/uptrace/bunrouter"
	"golang.org/x/time/rate"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// excludedPrefixes lists paths exempt from both rate and concurrency limiting.
var excludedPrefixes = []string{ //nolint:gochecknoglobals // package-level constant list
	"/api/v1/workers/",
	"/api/v1/heartbeat/",
	"/api/mgmt/health",
	"/metrics",
}

// ipEntry holds per-IP rate-limiter and concurrency semaphore state.
type ipEntry struct {
	limiter  *rate.Limiter
	sem      chan struct{}
	lastSeen time.Time
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

	entry := &ipEntry{
		limiter:  lim,
		sem:      sem,
		lastSeen: time.Now(),
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

// RateLimit is a bunrouter middleware that enforces the per-IP token-bucket rate limit.
func (rl *RateLimiter) RateLimit(next bunrouter.HandlerFunc) bunrouter.HandlerFunc {
	return func(writer http.ResponseWriter, req bunrouter.Request) error {
		if rl.cfg.RequestsPerMinute == 0 || isExcluded(req.URL.Path) {
			return next(writer, req)
		}

		ip := extractIP(req.Request, rl.cfg.TrustedProxies)
		entry := rl.getEntry(ip)

		if !entry.limiter.Allow() {
			prommetrics.HTTPRateLimited.WithLabelValues("rate").Inc()
			writeLimitError(writer, base.ErrorCodeRateLimited,
				"Too many requests",
				"Rate limit exceeded, please slow down",
			)
			return nil
		}

		return next(writer, req)
	}
}

// ConcurrencyLimit is a bunrouter middleware that enforces the per-IP concurrency limit.
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
		default:
			prommetrics.HTTPRateLimited.WithLabelValues("concurrency").Inc()
			writeLimitError(writer, base.ErrorCodeConcurrencyLimited,
				"Too many concurrent requests",
				"Too many simultaneous requests from your IP",
			)
			return nil
		}

		return next(writer, req)
	}
}
