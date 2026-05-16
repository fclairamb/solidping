# Performance Instrumentation & Load Harness

## Context

SolidPing's goal is to maximize checks-per-minute on both SQLite (self-hosted) and PostgreSQL (SaaS) backends. The current observability surface is good for **product** metrics (`solidping_check_executions_total`, `solidping_check_duration_seconds`, rate-limit counters) but **blind** to the signals you need for performance debugging:

- No DB pool stats (`sql.DB.Stats()` is never exposed). SQLite is pinned at `MaxOpenConns(1)` ([server/internal/db/sqlite/sqlite.go:120](server/internal/db/sqlite/sqlite.go:120)) — the single most important number under load is whether writers are queueing on this connection, and we can't see it.
- No per-stage timing inside a check's lifetime (fetch → claim → execute → save_result → process_incident → release_lease). The Bun sloghook logs query duration to slog but emits no histograms ([server/internal/db/sloghook/hook.go](server/internal/db/sloghook/hook.go)).
- No queue-depth gauge on `jobsChan` or `availableRunners` ([server/internal/checkworker/worker.go:66](server/internal/checkworker/worker.go:66)). Runner pool is fixed at 5 ([worker.go:84](server/internal/checkworker/worker.go:84)) with no visibility into saturation.
- No HTTP per-route latency histogram. The OTel SDK is wired ([server/internal/otelsetup/otelsetup.go](server/internal/otelsetup/otelsetup.go)) but no middleware actually starts spans.
- No lock-contention counters (SQLite `SQLITE_BUSY` retries, PG `FOR UPDATE SKIP LOCKED` empty claims).
- No reproducible load generator. Without one, optimization is theater.

This spec adds the missing instrumentation and builds a load harness. **No optimization changes.** A follow-up spec — written after we have baseline numbers — drives the actual fixes.

## Decisions (confirmed with user)

- Scope: instrumentation + load harness only. No optimizations in this spec.
- Backend: strict parity across SQLite and PostgreSQL.
- Surfaces: `/metrics`, pprof, and OTel are **each independently toggleable** via env var.

## Phase 1 — Instrumentation

### 1a. New Prometheus metrics

Add to [server/internal/prommetrics/metrics.go](server/internal/prommetrics/metrics.go):

- `solidping_db_pool_connections{backend, state}` **gauge** — `state` ∈ {open, in_use, idle, wait_count, wait_duration_seconds, max_idle_closed, max_lifetime_closed}. Exposed via a custom `prometheus.Collector` that calls `db.Stats()` at scrape time. New file: `server/internal/prommetrics/db_collector.go`.
- `solidping_db_query_duration_seconds{operation, backend, status}` **histogram** — `operation` ∈ {SELECT, INSERT, UPDATE, DELETE, TX}. Emitted from the sloghook (1c). Status is `ok` or `error`. Default buckets: `0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5`.
- `solidping_db_busy_retries_total{backend}` **counter** — incremented when sloghook sees a `SQLITE_BUSY` or PG serialization-failure error.
- `solidping_check_stage_duration_seconds{stage}` **histogram** — `stage` ∈ {fetch, claim, execute, save_result, process_incident, release_lease}. Buckets: `0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 30`.
- `solidping_worker_jobs_channel_depth{worker_uid}` **gauge** — `len(jobsChan)` sampled by a Collector at scrape time. Lives alongside the existing `solidping_worker_free_runners` gauge.
- `solidping_http_request_duration_seconds{method, route, status}` **histogram** — per-route latency for the public API. Buckets: `0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10`.
- `solidping_http_requests_total{method, route, status}` **counter** — sibling counter for the histogram.
- `solidping_claim_jobs_result_total{outcome}` **counter** — `outcome` ∈ {jobs, empty, lock_conflict}. Lets us distinguish "no due jobs" from "due jobs were locked by another worker".

Before adding `worker_lease_wait_seconds`: verify the existing `solidping_check_scheduling_delay_seconds` histogram ([prommetrics/metrics.go:~110](server/internal/prommetrics/metrics.go)) actually measures `claimed_at - scheduled_at`. If yes, reuse it. If it measures something else (e.g., dispatch latency), rename or add the new histogram.

### 1b. HTTP timing middleware

- Create `server/internal/middleware/metrics.go` with a `MetricsMiddleware` that wraps the response writer to capture status code and records `solidping_http_request_duration_seconds` + `solidping_http_requests_total` on `defer`.
- Route templating: use `chi.RouteContext(r.Context()).RoutePattern()` (or the equivalent for the router in use — check [server/internal/app/server.go:289](server/internal/app/server.go:289)) to get the *pattern* (`/api/v1/orgs/{org}/checks/{slug}`), not the raw path. Cardinality matters.
- Insert into the chain at [server/internal/app/server.go:289-291](server/internal/app/server.go:289), after `loggingMiddleware`, before `rateLimiter.RateLimit`. Order matters: we want the histogram to include time spent in rate-limit checks but not in CORS preflight handling (CORS short-circuits before us).
- When OTel is enabled, the same middleware starts a span. Gate via the existing `OTelConfig.Enabled`.

### 1c. DB sloghook → metrics

Extend [server/internal/db/sloghook/hook.go](server/internal/db/sloghook/hook.go)'s `AfterQuery`:

- Parse operation from `event.IQuery.Operation()` (Bun exposes this) or the leading verb of `event.Query`.
- Compute duration from `event.StartTime`.
- Call `prommetrics.RecordDBQuery(operation, backend, duration, err)`.
- Classify error: `errors.Is(err, sqlite3.ErrBusy)` (via `modernc.org/sqlite` or `mattn/go-sqlite3` — check the actual driver in use), or string-match for `serialization_failure` on pgx. Increment `solidping_db_busy_retries_total` accordingly.
- Add the hook variant to **both** [server/internal/db/postgres/postgres.go:96](server/internal/db/postgres/postgres.go:96) and [server/internal/db/sqlite/sqlite.go:75](server/internal/db/sqlite/sqlite.go:75) — they already share the sloghook.
- Pass `backend` label via a hook constructor argument so the hook doesn't have to detect dialect at runtime per call.

### 1d. Stage timing in worker

In [server/internal/checkworker/worker.go](server/internal/checkworker/worker.go):

- `executeJob()` (line 425): wrap each phase with `time.Now()` and emit `solidping_check_stage_duration_seconds{stage=...}` for `execute`, `save_result`, `process_incident`, `release_lease`.
- `fetcherLoop` (line 214): time the `selectAvailableJobs` call separately and emit as `fetch`.
- `ClaimJobs` (in [checkjobsvc/service.go:63](server/internal/checkworker/checkjobsvc/service.go:63)): time the claim TX and emit as `claim`. Also increment `solidping_claim_jobs_result_total` with the appropriate outcome.

Keep the additions surgical — a `defer` with `time.Since(start)` per phase, no refactor.

### 1e. Toggleable surfaces

Three independent env vars, each defaulting to safe values:

- `SP_METRICS_ENABLED` (default `true`) — gates the `/metrics` HTTP handler registration at [server/internal/app/server.go:792](server/internal/app/server.go:792). When false, the route 404s. Metrics collection itself stays on — only the endpoint is gated. (Cheap, no cost to "collect but not expose".)
- `SP_PROFILER_ENABLED` (default `false`) — already exists ([server/internal/profiler/profiler.go](server/internal/profiler/profiler.go)). Keep behavior. Listens on a separate port — document the listen address knob (`SP_PROFILER_LISTEN`).
- `SP_OTEL_ENABLED` (default `false`) — already exists ([server/internal/config/config.go](server/internal/config/config.go)). Keep behavior. The HTTP/DB instrumentation added in 1b/1c queries this flag at runtime to decide whether to start spans (metrics are always recorded; only spans are gated).

Document all three in [CLAUDE.md](CLAUDE.md) alongside the existing rate-limiting block.

## Phase 2 — Load Harness

### 2a. New binary: `cmd/loadgen/main.go`

A self-contained, reproducible loadgen. Not a benchmark in the `go test -bench` sense — Go benchmarks aren't a good fit for "checks/min over N minutes with network IO".

Flags:
- `--backend {sqlite|postgres}` (required)
- `--dsn STRING` (PG only; if omitted, spin up an embedded PG via the existing `embeddedpostgres` mechanism at [server/internal/db/postgres/postgres.go](server/internal/db/postgres/postgres.go))
- `--checks N` (default 100)
- `--duration M` (default `2m`)
- `--target-latency D` (default `10ms`) — the in-process target server's response delay
- `--output PATH` (default `bench-results/$(date +%Y%m%d-%H%M%S)-$backend.md`)

Behavior:
1. Bootstrap a fresh DB (temp SQLite file or embedded PG).
2. Start `app.Server` in-process with `SP_METRICS_ENABLED=true`.
3. Start an in-process target HTTP server on an ephemeral port that returns 200 OK after `--target-latency`.
4. Create N HTTP checks via the typed Go API (call into the same handlers/services the REST API uses — bypass HTTP). Each check points at the target server with a 60s schedule.
5. Force-schedule all N for immediate dispatch (set `next_run_at = NOW()`).
6. Run for `--duration`, scraping `/metrics` every 10s.
7. At end: parse final `/metrics`, compute deltas, write a markdown report.

Report sections:
- **Headline**: achieved checks/min (executions counter delta / duration).
- **Stage timings**: p50/p95/p99 for each `solidping_check_stage_duration_seconds{stage}`.
- **DB pool**: peak `in_use`, total `wait_count`, total `wait_duration_seconds`.
- **DB queries**: p95 per operation, total `busy_retries`.
- **HTTP**: p95 for `claim-jobs` and `submit-result`.
- **Lock contention**: `claim_jobs_result_total{outcome}` breakdown.
- **Limits hit**: rate-limit counters delta.

### 2b. Makefile target

- `make bench-checks` — runs both backends sequentially:
  ```
  go run ./cmd/loadgen --backend sqlite
  go run ./cmd/loadgen --backend postgres
  ```
  Writes both reports under `bench-results/`. `.gitignore` the directory.

### 2c. Not in scope

- CI integration of the bench. The harness produces reports; running it on every PR is a separate decision.
- Comparison tool ("diff two reports"). Markdown is human-readable; until we have a stable baseline, comparison is premature.
- Dashboards in docker-compose. User chose minimal: just expose endpoints, don't add Grafana/Prometheus containers.

## Critical files

| File | Change |
| --- | --- |
| [server/internal/prommetrics/metrics.go](server/internal/prommetrics/metrics.go) | Add new metric definitions (1a) |
| [server/internal/prommetrics/recording.go](server/internal/prommetrics/recording.go) | Add `RecordDBQuery`, `RecordHTTPRequest`, `RecordCheckStage` helpers |
| `server/internal/prommetrics/db_collector.go` | **NEW**: `prometheus.Collector` wrapping `db.Stats()` |
| [server/internal/db/sloghook/hook.go](server/internal/db/sloghook/hook.go) | Emit metrics from `AfterQuery` (1c) |
| [server/internal/db/postgres/postgres.go:96](server/internal/db/postgres/postgres.go:96) | Register pool collector with backend label "postgres" |
| [server/internal/db/sqlite/sqlite.go:75](server/internal/db/sqlite/sqlite.go:75) | Register pool collector with backend label "sqlite" |
| [server/internal/checkworker/worker.go](server/internal/checkworker/worker.go) | Stage timing + channel-depth gauge (1d) |
| [server/internal/checkworker/checkjobsvc/service.go:63](server/internal/checkworker/checkjobsvc/service.go:63) | Claim outcome counter (1d) |
| [server/internal/app/server.go:289-291](server/internal/app/server.go:289) | Wire metrics middleware in chain (1b) |
| [server/internal/app/server.go:792](server/internal/app/server.go:792) | Gate `/metrics` on `SP_METRICS_ENABLED` (1e) |
| `server/internal/middleware/metrics.go` | **NEW**: `MetricsMiddleware` (1b) |
| [server/internal/config/config.go](server/internal/config/config.go) | Add `SP_METRICS_ENABLED` field (1e). Note koanf env-var quirk — env reader may need a manual entry for multi-word keys (per repo memory). |
| `cmd/loadgen/main.go` | **NEW**: loadgen binary (2a) |
| [Makefile](Makefile) | Add `bench-checks` target (2b) |
| [CLAUDE.md](CLAUDE.md) | Document the three toggles + the bench target |

## Verification

End-to-end smoke (after implementation):

1. **Build clean**: `make build && make lint`.
2. **Tests pass**: `make test`.
3. **Metrics on/off**:
   - `make dev` → `curl http://localhost:4000/metrics | grep solidping_db_pool` should show pool gauges.
   - Restart with `SP_METRICS_ENABLED=false` → same URL returns 404.
4. **pprof on/off**:
   - With `SP_PROFILER_ENABLED=true`, curl `http://localhost:6060/debug/pprof/` (or the configured listen address) — 200.
   - With it false — connection refused / 404.
5. **OTel on/off**:
   - With `SP_OTEL_ENABLED=true` pointing at an stdout exporter (or local Jaeger), confirm at least one HTTP span and one DB span emitted per check.
   - With it false, no span overhead (spot-check via pprof CPU profile).
6. **Bench runs end-to-end**:
   - `make bench-checks` produces two markdown reports under `bench-results/`.
   - Both reports have non-zero values in every section.
   - The SQLite report shows `db_pool.in_use ≤ 1` always (sanity check on the single-writer pool).
   - The PG report shows `db_pool.wait_count` ≥ 0 — even if zero, the field renders.
7. **No regression in product metrics**: existing `solidping_check_executions_total` and `solidping_check_duration_seconds` still match expected counts in the loadgen runs.

## Out of scope (explicit follow-ups)

- **Optimization spec.** Once we have one clean baseline report per backend, draft `specs/todos/YYYY-MM-DD-NN-throughput-fixes-{sqlite,postgres}.md` with concrete hypotheses and target deltas.
- **CI bench gating.** Decide whether to run bench-checks on PRs and fail on regression.
- **Continuous profiling exporter** (Pyroscope/Parca). On-demand pprof is sufficient until proven otherwise.
- **Grafana dashboards** as code. Worth doing once the optimization spec lands and we know which panels matter.
