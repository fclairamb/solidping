// Package middleware provides HTTP middleware for authentication and authorization.
package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// ipEntry holds per-bucket rate-limiter and concurrency state. A bucket is
// keyed either by client IP (anonymous traffic) or by a hash of the
// presented bearer token (authenticated traffic) — see bucketFor.
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

// entryIdleTTL is how long an idle bucket (and an IP's token-key registry)
// survives before the cleanup loop evicts it.
const entryIdleTTL = 5 * time.Minute

// tokenSet tracks the distinct token bucket keys recently seen from one
// client IP, capping how many live token buckets a single IP can mint
// (cfg.TokenBucketsPerIP). Guarded by mu; pruned lazily on cap pressure and
// evicted wholesale by cleanupLoop once idle.
type tokenSet struct {
	mu       sync.Mutex
	keys     map[string]time.Time // token bucket key -> last seen
	lastSeen time.Time
}

// RateLimiter provides per-client rate limiting and concurrency limiting
// middleware. Buckets are per bearer token for authenticated traffic and
// per IP for anonymous traffic.
type RateLimiter struct {
	entries   sync.Map // bucket key -> *ipEntry
	tokenSets sync.Map // client IP -> *tokenSet
	cfg       config.RateLimitConfig
}

// NewRateLimiter creates a RateLimiter and starts its background cleanup goroutine.
//
//nolint:revive // ctx after cfg is intentional here
func NewRateLimiter(cfg config.RateLimitConfig, ctx context.Context) *RateLimiter {
	rl := &RateLimiter{cfg: cfg}
	go rl.cleanupLoop(ctx)
	return rl
}

func (rl *RateLimiter) getEntry(key string) *ipEntry {
	if val, ok := rl.entries.Load(key); ok {
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
	actual, _ := rl.entries.LoadOrStore(key, entry)
	if typedEntry, ok := actual.(*ipEntry); ok {
		return typedEntry
	}
	return entry
}

// cleanupLoop evicts buckets and token-key registries idle for more than
// entryIdleTTL, running every 2 minutes.
func (rl *RateLimiter) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-entryIdleTTL)
			rl.entries.Range(func(k, val any) bool {
				if entry, ok := val.(*ipEntry); ok && entry.lastSeen.Before(cutoff) {
					rl.entries.Delete(k)
				}
				return true
			})
			rl.tokenSets.Range(func(key, val any) bool {
				set, ok := val.(*tokenSet)
				if !ok {
					rl.tokenSets.Delete(key)
					return true
				}
				set.mu.Lock()
				idle := set.lastSeen.Before(cutoff)
				set.mu.Unlock()
				if idle {
					rl.tokenSets.Delete(key)
				}
				return true
			})
		}
	}
}

// realtimeStreamSuffix matches the long-lived org hint WebSocket
// (/api/v1/orgs/:org/events/ws). The connection must bypass both the request
// timeout (a held-open WS response would be killed at MaxRequestDuration and
// pin the timeout middleware goroutine) and the per-IP rate/concurrency
// limits (each open tab would permanently occupy a concurrency slot). The
// endpoint carries its own guards: realtime.max_connections and
// realtime.max_subscriptions_per_connection.
const realtimeStreamSuffix = "/events/ws"

func isExcluded(path string) bool {
	if !strings.HasPrefix(path, limitedPrefix) {
		return true
	}
	for _, prefix := range excludedPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	if strings.HasPrefix(path, "/api/v1/orgs/") && strings.HasSuffix(path, realtimeStreamSuffix) {
		return true
	}
	return false
}

// extractIPFromXFF parses X-Forwarded-For and returns the real client IP.
// Each proxy hop appends the peer address it accepted the connection from,
// so with N trusted hops the client is the Nth entry from the right —
// parts[len(parts)-trustedProxies] (0-indexed). When the header carries
// fewer entries than trusted hops (e.g. a single-entry header behind one
// proxy that overwrote a client-supplied value), the left-most entry is the
// best available answer, so the index is clamped to 0.
func extractIPFromXFF(xff string, trustedProxies int) string {
	if trustedProxies <= 0 {
		return ""
	}
	parts := strings.Split(xff, ",")
	idx := len(parts) - trustedProxies
	if idx < 0 {
		idx = 0
	}
	return strings.TrimSpace(parts[idx])
}

// extractIP returns the client IP, respecting the configured TrustedProxies
// count.
//
// Trust model: setting TrustedProxies > 0 asserts that the deployment sits
// behind that many proxy hops which *strip or overwrite* client-supplied
// X-Forwarded-For / X-Real-IP headers (traefik and most managed ingresses
// do; verify before enabling behind a bare ALB or default nginx). X-Real-IP
// is only consulted when X-Forwarded-For is absent entirely — a present but
// unparsable XFF falls through to RemoteAddr, never to the equally
// client-controllable X-Real-IP.
func extractIP(req *http.Request, trustedProxies int) string {
	if trustedProxies > 0 {
		if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
			if ip := extractIPFromXFF(xff, trustedProxies); ip != "" {
				return ip
			}
		} else if xri := req.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}

	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}
	return host
}

// Bucket-kind labels for the caller's rate/concurrency bucket, reported by
// /api/mgmt/limits.
const (
	// BucketKindIP marks a bucket keyed by client IP (anonymous traffic, or
	// token traffic past the per-IP token-bucket cap).
	BucketKindIP = "ip"
	// BucketKindToken marks a bucket keyed by a hash of the presented
	// bearer token (authenticated traffic).
	BucketKindToken = "token"
)

// admitTokenKey records tokenKey as live for ip and reports whether the IP
// is still under its token-bucket cap. Keys idle for more than entryIdleTTL
// are pruned lazily when the cap is hit, so token rotation (e.g. hourly JWT
// refresh) doesn't permanently consume cap slots.
func (rl *RateLimiter) admitTokenKey(ip, tokenKey string) bool {
	val, _ := rl.tokenSets.LoadOrStore(ip, &tokenSet{keys: make(map[string]time.Time)})
	set, ok := val.(*tokenSet)
	if !ok {
		return false
	}

	now := time.Now()
	set.mu.Lock()
	defer set.mu.Unlock()
	set.lastSeen = now

	if _, seen := set.keys[tokenKey]; seen {
		set.keys[tokenKey] = now
		return true
	}
	if len(set.keys) >= rl.cfg.TokenBucketsPerIP {
		cutoff := now.Add(-entryIdleTTL)
		for k, last := range set.keys {
			if last.Before(cutoff) {
				delete(set.keys, k)
			}
		}
	}
	if len(set.keys) >= rl.cfg.TokenBucketsPerIP {
		return false
	}
	set.keys[tokenKey] = now
	return true
}

// bucketFor resolves the rate/concurrency bucket for a request, returning
// (bucketKey, bucketKind, clientIP) — the kind is BucketKindIP or
// BucketKindToken.
//
// Authenticated traffic is keyed by a stable hash of the presented bearer
// token rather than by client IP, so a team behind one shared NAT/VPN
// egress doesn't collapse into a single bucket. The token is NOT verified
// here — the limiter runs before auth — which is acceptable for rate
// limiting: the goal is fair-share isolation, not authentication. To keep
// "mint a random token, get a fresh bucket" from becoming an IP-limit
// bypass, each client IP may hold at most cfg.TokenBucketsPerIP live token
// buckets; tokens beyond the cap (and anonymous or malformed-auth requests)
// fall back to the shared per-IP bucket, so a single IP is bounded to
// (cap+1)x the per-IP allowance. The "t:" key prefix cannot collide with an
// IP key: 't' is not a valid IPv4/IPv6 character.
func (rl *RateLimiter) bucketFor(req *http.Request) (string, string, string) {
	ip := extractIP(req, rl.cfg.TrustedProxies)
	if rl.cfg.TokenBucketsPerIP <= 0 {
		return ip, BucketKindIP, ip
	}
	token := extractToken(req)
	if token == "" {
		return ip, BucketKindIP, ip
	}
	sum := sha256.Sum256([]byte(token))
	tokenKey := "t:" + hex.EncodeToString(sum[:8])
	if !rl.admitTokenKey(ip, tokenKey) {
		return ip, BucketKindIP, ip
	}
	return tokenKey, BucketKindToken, ip
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

		key, _, _ := rl.bucketFor(req.Request)
		entry := rl.getEntry(key)

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

// CallerBucket resolves the caller's bucket exactly like the limiting
// middlewares do, returning (bucketKey, bucketKind, clientIP) — the key
// feeds StateFor, the kind is BucketKindIP or BucketKindToken. Exposed for
// the /api/mgmt/limits introspection handler.
func (rl *RateLimiter) CallerBucket(req *http.Request) (string, string, string) {
	return rl.bucketFor(req)
}

// StateFor returns a snapshot of a bucket's rate-limit and concurrency state
// without consuming a token or acquiring a semaphore slot. If the bucket has
// no entry yet, it returns the "fresh bucket" defaults (full burst, zero
// in-flight).
func (rl *RateLimiter) StateFor(key string) IPState {
	state := IPState{
		Remaining: float64(rl.cfg.Burst),
		InFlight:  0,
	}

	val, ok := rl.entries.Load(key)
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

		key, _, _ := rl.bucketFor(req.Request)
		entry := rl.getEntry(key)

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
