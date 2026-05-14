# Per-IP HTTP rate limiting and concurrency limiting

## Honest assessment of the original proposal

The request asked for two middlewares with defaults of `nbReqPerMin=100` and
`nbConReq=5`. After looking at the codebase I think both defaults are wrong for
this product, and the "wait a bit" behaviour needs a design decision. The spec
below captures the reasoning and picks different values.

### Why 100 req/min is too low

A cold dashboard page load fires roughly 8–12 simultaneous API calls (checks,
incidents, channels, members, features, me, results, etc.). A user who opens the
dashboard and navigates a few times will hit 100 req/min in under 2 minutes.
Workers also hit `/api/v1/workers/heartbeat` and `/api/v1/workers/claim-jobs`
repeatedly, though those are excluded (see below).

**Recommended default: 300 req/min (5 req/s sustained) with a burst of 60.**
This allows a page load spike without tripping the limit and still caps a
runaway scraper.

### Why 5 concurrent is too low

In HTTP/1.1, browsers open up to 6 parallel connections to the same origin.
Even with HTTP/2 multiplexing, a dashboard page emits 10+ requests in parallel
on mount. A limit of 5 means a normal page load will always queue some requests.
20 concurrent is the right starting point for a dashboard product.

**Recommended default: 20 concurrent per IP.**

### "Wait a bit" — delay vs reject

There are two ways to handle over-limit requests:

| Mode | Behaviour | Trade-off |
|---|---|---|
| Reject (429) | Return immediately, `Retry-After` header set | Client-friendly, no goroutine held |
| Delay | Hold the connection, release when quota refills | Feels smoother to end users, but holds goroutines and amplifies DDoS |

Holding connections on an over-limit IP doubles down — one slow/abusive IP can
now starve the goroutine pool. **Reject with 429 + `Retry-After` is the right
choice.** The client knows exactly when to retry, and we shed the load
immediately.

If we ever want nginx-style delay-then-reject (allow small queuing up to a
burst, then reject), that can be layered on top as a future option.

### Excluded routes

Some routes have fundamentally different traffic patterns and must not count
against the general rate limit:

- **`/api/v1/workers/*`** — distributed worker pool. Multiple workers may share
  a NAT IP, and they heartbeat every few seconds.
- **`/api/v1/heartbeat/:org/:identifier`** — designed to receive high-frequency
  pings from external systems; rate-limiting this defeats its purpose.
- **`/api/mgmt/health`** — polled by load balancers; must not be restricted.
- **`/metrics`** — Prometheus scrape; same reasoning.

The concurrent limiter similarly must skip these paths (a slow worker claim
holding a slot would be silly).

### IP extraction and trusted proxies

The current code uses `req.RemoteAddr` for nothing rate-limit-related yet. If
SolidPing runs behind a reverse proxy (nginx, k8s ingress, Cloudflare), the
real client IP is in `X-Forwarded-For` or `X-Real-IP`. We need a
**trusted proxy** knob:

- When `0` (default for self-hosted direct): use `RemoteAddr` directly.
- When `N > 0`: strip the last N addresses from `X-Forwarded-For` (those are
  the trusted proxy hops) and use the leftmost remaining address.

Trusting those headers unconditionally without a configured proxy count is an
IP-spoofing vector — anyone can set `X-Forwarded-For: 1.2.3.4` and bypass a
ban targeting their real IP.

---

## Context

SolidPing exposes public-ish API surface: public status pages, heartbeat
ingestion, badge endpoints, OAuth callbacks. Self-hosted installs are on the
open internet. Without IP-level limiting, a single misbehaving client can
saturate the server goroutine pool or exhaust DB connections.

The two middlewares are independent and serve different threat models:

- **Rate limiter** — prevents high-frequency polling / scraping / enumeration.
  Token-bucket per IP, returning 429 when the bucket is empty.
- **Concurrency limiter** — prevents a single slow IP from holding goroutines
  open (e.g., slow-loris style, or many long-running SSE connections). Semaphore
  per IP, returning 429 when the semaphore is full.

---

## Configuration

Add a `RateLimiting` struct to `ServerConfig` (config.go):

```go
type RateLimitConfig struct {
    // RequestsPerMinute is the token-bucket refill rate per IP per minute.
    // 0 disables the rate limiter entirely.
    RequestsPerMinute int `koanf:"requests_per_minute"`

    // Burst is the maximum instantaneous burst above the sustained rate.
    // Defaults to RequestsPerMinute / 5 (one-fifth of a minute's allowance).
    Burst int `koanf:"burst"`

    // MaxConcurrent is the maximum number of in-flight requests per IP.
    // 0 disables the concurrency limiter entirely.
    MaxConcurrent int `koanf:"max_concurrent"`

    // TrustedProxies is the number of trusted reverse-proxy hops.
    // 0 means use RemoteAddr directly (safe default for direct deployments).
    // Set to 1 if behind a single nginx/ingress that sets X-Forwarded-For.
    TrustedProxies int `koanf:"trusted_proxies"`
}
```

**Defaults:**
```go
RateLimiting: RateLimitConfig{
    RequestsPerMinute: 300,
    Burst:             60,
    MaxConcurrent:     20,
    TrustedProxies:    0,
}
```

**Environment variable overrides** (koanf convention):
```
SP_SERVER_RATE_LIMITING_REQUESTS_PER_MINUTE=300
SP_SERVER_RATE_LIMITING_BURST=60
SP_SERVER_RATE_LIMITING_MAX_CONCURRENT=20
SP_SERVER_RATE_LIMITING_TRUSTED_PROXIES=1
```

Setting `RequestsPerMinute=0` or `MaxConcurrent=0` individually disables that
limiter, making both opt-out independently.

---

## Implementation

### New file: `server/internal/middleware/ratelimit.go`

```
package middleware

// RateLimiter is a per-IP token-bucket rate limiter middleware.
// Uses golang.org/x/time/rate — already available as a stdlib transitive dep.
// Per-IP state is stored in a sync.Map; a background goroutine evicts entries
// idle for more than 5 minutes to prevent unbounded memory growth.

// ConcurrencyLimiter is a per-IP semaphore middleware.
// Each IP gets a counting semaphore of size MaxConcurrent.
// Requests that cannot acquire immediately get 429 (no delay).
```

**Key types:**

```go
type ipEntry struct {
    limiter  *rate.Limiter // for rate limiting
    sem      chan struct{}  // for concurrency limiting (buffered channel as semaphore)
    lastSeen time.Time
}
```

Store per-IP entries in a single `sync.Map[string, *ipEntry]` shared between
both middlewares (same IP key, same entry), so we only do one map lookup per
request.

**IP extraction helper** respects `TrustedProxies`:

```go
func extractIP(r *http.Request, trustedProxies int) string
```

**Cleanup goroutine** — start from the middleware constructor, evict entries
where `lastSeen > 5 minutes ago`. Run every 2 minutes. Stop when ctx is done
(pass a context to the constructor).

### Wiring in `server.go`

Apply the middlewares on `mainGroup` (the top-level group that all routes
share), but *after* CORS and Sentry, *before* auth. They should apply globally
with path-based exclusions checked inside the middleware:

```go
rl := middleware.NewRateLimiter(cfg.Server.RateLimiting, ctx)
mainGroup = mainGroup.Use(rl.RateLimit).Use(rl.ConcurrencyLimit)
```

Excluded paths (checked inside the middleware with `strings.HasPrefix`):
- `/api/v1/workers/`
- `/api/v1/heartbeat/`
- `/api/mgmt/health`
- `/metrics`

Alternative: register the two middlewares only on specific sub-groups (the
API group). This is cleaner than path-prefix checks inside the middleware but
means static files and the dash are also unprotected — acceptable since those
are served from embedded FS and can't cause harm. **Recommendation: apply to
`mainGroup` with path exclusions, so static-asset flooding is also caught.**

### Error response

On rate limit exceeded:

```http
HTTP/1.1 429 Too Many Requests
Content-Type: application/json
Retry-After: 60

{"title":"Too many requests","code":"RATE_LIMITED","detail":"Rate limit exceeded, please slow down"}
```

On concurrency limit exceeded:

```http
HTTP/1.1 429 Too Many Requests
Content-Type: application/json

{"title":"Too many concurrent requests","code":"CONCURRENCY_LIMITED","detail":"Too many simultaneous requests from your IP"}
```

Both use the existing `base.HandlerBase` error format. Add two new error codes:
`RATE_LIMITED` and `CONCURRENCY_LIMITED`.

---

## Prometheus metrics

Add two counters to `internal/prommetrics/`:

```
solidping_http_rate_limited_total{reason="rate"} — incremented on each 429 from rate limiter
solidping_http_rate_limited_total{reason="concurrency"} — incremented on each 429 from concurrency limiter
```

This lets operators detect misconfiguration (e.g., limit set too low, alerting
on a sudden spike of 429s) without digging through logs.

---

## Testing

### Unit tests (`middleware/ratelimit_test.go`)

- Rate limiter: send N+1 requests from same IP, assert N succeed and 1 gets 429
- Concurrency limiter: hold N requests open (using a test handler that blocks
  on a channel), send N+1th, assert 429 immediately
- Excluded paths: `/api/mgmt/health` never gets 429 regardless of rate
- IP extraction: verify `TrustedProxies=1` reads the correct address from
  `X-Forwarded-For`

No integration test needed — unit tests with `httptest.NewRecorder` suffice.

---

## Acceptance criteria

- [ ] `RateLimitConfig` added to `ServerConfig`, defaults to 300 RPM / burst 60 / 20 concurrent / 0 trusted proxies
- [ ] Both limits configurable to 0 to disable independently
- [ ] Both middlewares live in `internal/middleware/ratelimit.go`
- [ ] `/api/v1/workers/`, `/api/v1/heartbeat/`, `/api/mgmt/health`, `/metrics` excluded from both limits
- [ ] Over-limit requests return 429 with correct `code` field immediately (no delay)
- [ ] Rate-limited responses include `Retry-After: 60` header
- [ ] Prometheus counters emitted per limit type
- [ ] Cleanup goroutine evicts idle entries; no unbounded memory growth
- [ ] `TrustedProxies` knob controls X-Forwarded-For parsing
- [ ] All unit tests pass; `make lint` passes

## Files affected

- `server/internal/config/config.go` — add `RateLimitConfig`, wire into `ServerConfig` defaults
- `server/internal/middleware/ratelimit.go` — new file, both middlewares + IP extraction + cleanup
- `server/internal/middleware/ratelimit_test.go` — new file
- `server/internal/app/server.go` — wire middlewares into `mainGroup`
- `server/internal/handlers/base/errors.go` (or wherever error codes live) — add `RATE_LIMITED`, `CONCURRENCY_LIMITED`
- `server/internal/prommetrics/metrics.go` — add the rate-limit counter
- `docs/` or `CLAUDE.md` — document the new env vars in the deployment section

## Implementation Plan

1. **Config**: Add `RateLimitConfig` struct to `server/internal/config/config.go` and wire into `ServerConfig` with defaults (300 RPM, burst 60, 20 concurrent, 0 trusted proxies).
2. **Error codes**: Add `ErrorCodeConcurrencyLimited` to `server/internal/handlers/base/base.go` (note: `ErrorCodeRateLimited` already exists).
3. **Prometheus metric**: Add `HTTPRateLimited` counter vec with `reason` label to `server/internal/prommetrics/metrics.go`.
4. **Middleware**: Create `server/internal/middleware/ratelimit.go` with:
   - `ipEntry` struct holding rate limiter + semaphore + lastSeen
   - `RateLimiter` struct with `sync.Map`, config, and context
   - `extractIP()` helper respecting `TrustedProxies`
   - `RateLimit()` middleware handler
   - `ConcurrencyLimit()` middleware handler
   - Background cleanup goroutine (evict entries idle >5 min, run every 2 min)
5. **Tests**: Create `server/internal/middleware/ratelimit_test.go` with unit tests for rate limiting, concurrency limiting, excluded paths, and IP extraction.
6. **Wire**: Update `server/internal/app/server.go` to instantiate and apply both middlewares on `mainGroup`.
7. **Docs**: Document new env vars in `CLAUDE.md` deployment section.
