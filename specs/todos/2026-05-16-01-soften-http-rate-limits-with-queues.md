# Soften HTTP per-IP limiting with bounded queues

## Context

The current per-IP middleware stack (`server/internal/middleware/ratelimit.go`) is **binary** — either you have a token / a free concurrency slot or you get an immediate `429`. For a dashboard that triggers many parallel `GET`s on a page load (results, incidents, status, jobs, …) this is too aggressive: a normal user clicking around hits `429` even with sane limits.

This spec softens both middlewares with a **small bounded waiting room** before rejecting, surfaces wait time in response headers so the dashboard can show "we held this request for X ms" rather than failing cold, and introduces a server-wide **30 s max request duration** as a separate safety net.

### Honest opinion (recorded at planning time)

Queueing in front of rate/concurrency limits is a well-known pattern and dramatically improves UX for a UI-driven product — the right tradeoff for SolidPing. Caveats:

1. **Per-IP queueing raises the per-IP goroutine ceiling** from `MaxConcurrent` (default 20) to `MaxConcurrent + ConcurrencyQueue + RateQueue` (default 30). A noisy client (or spoofed `X-Forwarded-For` when proxies are misconfigured) ties up more resources. Acceptable for a self-hosted/small-SaaS profile.
2. **A queue without an explicit max-wait can hold clients well past their fetch timeout.** Middlewares **must** honor `req.Context().Done()` and the configured hard ceiling.
3. **Queued requests must not loop.** A request that waited 29 s and still fails the token bucket gets a 429 — never re-queued.

## Goals

- **Concurrency middleware**: while ≤10 in-flight per IP, pass through; while 11–20 (10 active + up to 10 queued), wait for a slot; >20, return `429`.
- **Rate middleware**: if a token is available, pass through; otherwise allow up to 10 requests per IP to wait in a "slow lane" for the next refill; if the slow lane is full, return `429`.
- **Both middlewares**: when a request waited, set a response header reporting how long it was held.
- **Hard ceiling on wait time** (`30 s`); on timeout/client-cancel, return `429` with `Retry-After: 60`.
- **Server-wide max request duration** (`30 s`) covering queue wait + handler execution. Exceeding it returns `504` with `REQUEST_TIMEOUT`.
- **Metrics extended** so operators can distinguish "passed after wait" from "rejected".

## Non-goals

- No global queue. Limits stay per-IP. (Otherwise one noisy IP starves the rest.)
- No retry/backoff smarts inside the middleware — `Retry-After: 60` stays on hard rejections only.
- No change to excluded prefixes (`/api/v1/workers/`, `/api/v1/heartbeat/`, `/api/mgmt/`, `/metrics`).
- No change to middleware order: `RequestTimeout` → `RateLimit` → `ConcurrencyLimit`. The timeout budget covers everything downstream.

## Design

### Config additions

`server/internal/config/config.go` — `RateLimitConfig` (around line 285):

```go
// RateQueue is the number of requests per IP that may wait for the next
// rate-limit token before being rejected. 0 disables queueing.
RateQueue int `koanf:"rate_queue"` // default 10

// ConcurrencyQueue is the number of requests per IP that may wait for a
// concurrency slot before being rejected. 0 disables queueing.
ConcurrencyQueue int `koanf:"concurrency_queue"` // default 10

// MaxQueueWait is the hard ceiling on how long a request may sit in either
// queue before being rejected with 429. 0 disables the ceiling.
MaxQueueWait time.Duration `koanf:"max_queue_wait"` // default 30s
```

`ServerConfig` (same file):

```go
// MaxRequestDuration is the total per-request budget covering queue wait
// + handler execution. Exceeded requests return 504 REQUEST_TIMEOUT.
// 0 disables the timeout.
MaxRequestDuration time.Duration `koanf:"max_request_duration"` // default 30s
```

Environment variables (per `project_koanf_env_quirk.md` — manual reader needed for multi-word keys):

- `SP_SERVER_RATE_LIMITING_RATE_QUEUE`
- `SP_SERVER_RATE_LIMITING_CONCURRENCY_QUEUE`
- `SP_SERVER_RATE_LIMITING_MAX_QUEUE_WAIT`
- `SP_SERVER_MAX_REQUEST_DURATION`

Wire all four through `applyRateLimitingEnv` / the equivalent server-scope helper.

### Server-wide request processing timeout

- New middleware `RequestTimeout` in `server/internal/middleware/timeout.go`.
- Mounted on `mainGroup` **before** `RateLimit` (`server/internal/app/server.go` around line 287) so the timeout budget covers queue wait too.
- Implementation: wrap `req.Request` with a context derived via `context.WithTimeout(req.Context(), cfg.MaxRequestDuration)`; on `ctx.Done()` before the handler returns, respond with `504` + `REQUEST_TIMEOUT` provided the writer hasn't already been used.
- Excluded prefixes: same list as the rate middleware (workers, heartbeat, mgmt, metrics).
- Add `ErrorCodeRequestTimeout = "REQUEST_TIMEOUT"` in `server/internal/handlers/base/`.
- Document `REQUEST_TIMEOUT` in `CLAUDE.md` error codes section.

### Per-IP state (`ipEntry` at ratelimit.go:32)

```go
type ipEntry struct {
    limiter     *rate.Limiter
    sem         chan struct{} // capacity = MaxConcurrent (active slots)
    rateQueue   chan struct{} // capacity = RateQueue (waiting on token)
    concurQueue chan struct{} // capacity = ConcurrencyQueue (waiting on sem)
    lastSeen    time.Time
}
```

Both `*Queue` channels are **counting semaphores for waiting-room admission**, not actual queues — the goroutine of the waiting request *is* its queue slot.

### Rate middleware (`RateLimit` at ratelimit.go:162)

```
1. Fast path: entry.limiter.Allow() → next(w, r)
2. Admission to slow lane:
       select {
         case entry.rateQueue <- struct{}{}: // got a waiting slot
         default: 429 "rate"
       }
3. ctx, cancel := context.WithTimeout(req.Context(), cfg.MaxQueueWait); defer cancel()
   reservation := entry.limiter.Reserve()
   if !reservation.OK() || reservation.Delay() > maxWaitRemaining:
       reservation.Cancel(); <-entry.rateQueue; return 429 "rate"
   wait := reservation.Delay()
   select {
     case <-time.After(wait):
         <-entry.rateQueue
         w.Header().Set("X-Rate-Limit-Delayed-Ms", strconv.FormatInt(wait.Milliseconds(), 10))
         return next(w, r)
     case <-ctx.Done():
         reservation.Cancel()
         <-entry.rateQueue
         return 429 "rate"
   }
```

Metric on admission to slow lane: `solidping_http_rate_limited_total{reason="rate_delayed"}` incremented when the waited request is admitted.
Metric on hard reject (queue full, ceiling hit, context cancel): existing `reason="rate"`.

### Concurrency middleware (`ConcurrencyLimit` at ratelimit.go:240)

```
1. Try sem non-blocking: succeed → next, defer release.
2. Try concurQueue non-blocking:
       select {
         case entry.concurQueue <- struct{}{}:
         default: 429 "concurrency"
       }
3. ctx, cancel := context.WithTimeout(req.Context(), cfg.MaxQueueWait); defer cancel()
   start := time.Now()
   select {
     case entry.sem <- struct{}{}:
         <-entry.concurQueue
         defer func() { <-entry.sem }()
         w.Header().Set("X-Concurrency-Queued-Ms",
             strconv.FormatInt(time.Since(start).Milliseconds(), 10))
         return next(w, r)
     case <-ctx.Done():
         <-entry.concurQueue
         return 429 "concurrency"
   }
```

Metric on admission after wait: `solidping_http_rate_limited_total{reason="concurrency_queued"}`.
Metric on hard reject: existing `reason="concurrency"`.

### Headers

- `X-Rate-Limit-Delayed-Ms` — successful response that waited for a rate token. Integer milliseconds.
- `X-Concurrency-Queued-Ms` — successful response that waited for a concurrency slot. Integer milliseconds.
- `Retry-After: 60` — unchanged on `429` (set by `writeLimitError`).

### Metric label expansion

`server/internal/prommetrics/metrics.go:132` — update help text; the `reason` label now carries four values:

- `rate` (existing, hard reject)
- `rate_delayed` (new, queued then admitted)
- `concurrency` (existing, hard reject)
- `concurrency_queued` (new, queued then admitted)

A histogram `solidping_http_limit_wait_seconds{kind="rate"|"concurrency"}` is a possible follow-up; explicitly out of scope here.

## Critical files

- `server/internal/middleware/ratelimit.go` — both middlewares, `ipEntry`, `getEntry`
- `server/internal/middleware/ratelimit_test.go` — add tests, preserve existing ones
- `server/internal/middleware/timeout.go` — **new** `RequestTimeout` middleware
- `server/internal/middleware/timeout_test.go` — **new** tests
- `server/internal/config/config.go` — `RateLimitConfig` + `ServerConfig` additions, `applyRateLimitingEnv`
- `server/internal/handlers/base/` — add `ErrorCodeRequestTimeout`
- `server/internal/app/server.go` around line 287 — wire `RequestTimeout` before `RateLimit`
- `server/internal/prommetrics/metrics.go:132` — help-text update
- `CLAUDE.md` — refresh "HTTP rate limiting (per-IP)" section: new env vars, the two new headers, `REQUEST_TIMEOUT` error code, server-wide max request duration

## Verification

### Unit tests in `ratelimit_test.go`

- `TestRateLimit_QueuesUntilRefill` — exhaust burst, fire 5 in parallel, all 5 succeed with `X-Rate-Limit-Delayed-Ms > 0` and increasing.
- `TestRateLimit_RejectsBeyondQueue` — exhaust burst, fire `Burst + RateQueue + 1` in parallel, last gets `429 rate` + `Retry-After: 60`.
- `TestRateLimit_HonorsContextCancel` — queue a request, cancel client context, assert `429` and queue slot released (fresh request admitted immediately).
- `TestRateLimit_HonorsMaxQueueWait` — `MaxQueueWait=50ms`, refill that needs `500ms`, assert `429` after ~50 ms.
- `TestConcurrencyLimit_QueuesUntilRelease` — `MaxConcurrent=2`, hold 2, fire 2 more, release the 2 active, queued 2 succeed with `X-Concurrency-Queued-Ms > 0`.
- `TestConcurrencyLimit_RejectsBeyondQueue` — `MaxConcurrent=2 ConcurrencyQueue=2`, hold 2, fire 3 more, last gets `429 concurrency`.
- `TestConcurrencyLimit_HonorsContextCancel` — queue a request, cancel context, slot released for next caller.

### Unit tests in `timeout_test.go`

- `TestRequestTimeout_Slow504` — handler sleeps past `MaxRequestDuration`, response is `504 REQUEST_TIMEOUT`.
- `TestRequestTimeout_FastUnaffected` — handler returns immediately, no header bloat, 200 unchanged.
- `TestRequestTimeout_ExcludedPaths` — worker / heartbeat / mgmt / metrics paths ignore the timeout middleware.

### Metric assertions

Add to two of the queueing tests: `rate_delayed` and `concurrency_queued` counters increment after the queued request is admitted.

### Lint

`make lint` — do not relax `.golangci.yml` to make CI pass (per `feedback_lint_strict.md`). If `gocognit` complains on the rate middleware, extract helpers rather than annotating.

### Manual smoke test against `make dev`

```bash
SP_SERVER_RATE_LIMITING_REQUESTS_PER_MINUTE=12 \
SP_SERVER_RATE_LIMITING_BURST=2 \
SP_SERVER_RATE_LIMITING_RATE_QUEUE=3 \
make dev

# In another shell:
seq 1 10 | xargs -P10 -I{} curl -sw '%{http_code} %{header_x-rate-limit-delayed-ms}\n' \
  -o /dev/null http://localhost:4000/api/v1/check-types
# Expect: 2 fast 200, 3 slow 200 with non-zero header, 5 × 429.

curl -s http://localhost:4000/metrics | grep solidping_http_rate_limited_total
# Expect: rate, rate_delayed, concurrency, concurrency_queued labels visible.
```

## Decisions (resolved at planning time)

- **Headers**: two specific headers — `X-Rate-Limit-Delayed-Ms` and `X-Concurrency-Queued-Ms`.
- **Max queue wait**: `30 s` default.
- **Server-wide max request duration**: `30 s` default — covers queue wait + handler execution as one budget.
- **Timeout response from rate-limit middlewares**: `429` with `Retry-After: 60` (consistent with hard rejects).
- **Timeout response from the new request-timeout middleware**: `504 Gateway Timeout` with new error code `REQUEST_TIMEOUT`.
