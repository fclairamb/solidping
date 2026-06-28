# In-depth memory consumption analysis: monitoring + code analysis

## Context

SolidPing is a long-running, multi-tenant, distributed monitoring system: an API
server plus N workers that execute checks on a sub-minute cadence, forever. That
shape is exactly where memory problems hide — slow leaks that only surface after
days of uptime, per-check allocation churn multiplied by thousands of executions
per minute, and in-memory structures that grow with org/check/IP count and are
never bounded. We want a **repeatable way to answer three questions** for both
the server and the worker role:

1. **Where does memory go?** (steady-state composition: heap by type, off-heap,
   goroutines)
2. **Does it grow without bound?** (leaks over time, under steady load)
3. **What scales with tenants/checks/workers?** (the structures that turn "fine
   in dev" into "OOM at scale")

The good news: most of the observability primitives already exist. This spec is
about **wiring the few missing pieces, writing down a profiling methodology, and
running it** — not a from-scratch build. It is deliberately a *mix of new APIs/
instrumentation (code) and a documented process*, as requested.

### What already exists (inventory)

- **pprof profiler** — [`server/internal/profiler/profiler.go`](server/internal/profiler/profiler.go),
  gated by `SP_PROFILER_ENABLED` / `SP_PROFILER_LISTEN` (default `localhost:6060`,
  [`config.go:475`](server/internal/config/config.go#L475)), wired at
  [`server.go:334`](server/internal/app/server.go#L334) and started at
  [`server.go:1663`](server/internal/app/server.go#L1663). It registers
  `pprof.Index` at `/debug/pprof/` — and because `Index` dispatches named
  profiles, **`/debug/pprof/heap`, `/goroutine`, `/allocs`, `/threadcreate` all
  already work** when enabled. CPU `profile`, `trace`, `symbol`, `cmdline` are
  registered explicitly. No auth (localhost-bound by default).
- **Prometheus** — `SP_PROMETHEUS_ENABLED` (default on), registered at
  [`server.go:1021`](server/internal/app/server.go#L1021) via
  `prommetrics.Register(...)`. Rich custom app metrics in
  [`prommetrics/metrics.go`](server/internal/prommetrics/metrics.go) (check
  executions/duration, worker free runners, jobs queue depth, incidents…). A
  **DB-pool collector** already mirrors the exact pattern we'll reuse —
  [`prommetrics/db_collector.go`](server/internal/prommetrics/db_collector.go)
  implements `prometheus.Collector` and reads `sql.DB.Stats()` at scrape time.
- **OpenTelemetry** — `SP_OTEL_ENABLED`
  ([`config.go:77`](server/internal/config/config.go#L77),
  [`otelsetup/otelsetup.go`](server/internal/otelsetup/otelsetup.go)); traces +
  OTLP metrics pipeline exists but exports no memory signals today.
- **Management endpoints** — `/api/mgmt/{health,version,limits,report}`
  ([`server.go:1014`](server/internal/app/server.go#L1014)), the established
  pattern for operational endpoints.
- **Load harness** — `make bench-checks` (knob `BENCH_CHECKS=N`) drives a
  `loadgen` binary against SQLite and Postgres test servers
  ([`Makefile:181`](Makefile#L181)). This is our memory-under-load generator.

### The gaps this spec closes

1. **No Go runtime / process collectors in Prometheus.** `go_memstats_*`,
   `process_resident_memory_bytes`, `go_goroutines`, `go_gc_*` are **not**
   registered — so there is no time series for heap/RSS/goroutine trends, which
   is precisely what leak detection needs.
2. **No memory-stats API.** `runtime.ReadMemStats`, `runtime.NumGoroutine`,
   `debug.ReadGCStats` are called nowhere; the EWMA
   [`stats/processingStats.go`](server/internal/stats/processingStats.go) is not
   exposed. There is no in-app way for a self-hosted operator to see memory.
3. **No visibility into the suspect long-lived structures** (DEK cache, rate-
   limiter IP map, event listeners, express goroutines) — their sizes are
   invisible to both metrics and profiles-at-a-glance.
4. **block/mutex profiling is dead.** `pprof.Index` exposes `/debug/pprof/block`
   and `/mutex`, but they return nothing because `runtime.SetBlockProfileRate` /
   `SetMutexProfileFraction` are never set (default 0 = off).
5. **No leak tests.** Nothing asserts that stopping a worker/server leaves zero
   leaked goroutines.
6. **Off-heap memory is unaccounted for.** The SQLite driver is
   `uptrace/bun/driver/sqliteshim`, which selects **modernc** (pure-Go,
   on-heap → shows in pprof) when CGO is off, or **mattn** (CGO, off-heap →
   invisible to pprof) when CGO is on. Whether SQLite memory appears in heap
   profiles depends on the build's `CGO_ENABLED`. This must be established
   up front, or the analysis will misattribute the `RSS − go_heap_inuse` gap.

## Goals

- A **standing memory-monitoring surface**: Go runtime + process metrics in
  Prometheus, custom gauges for the suspect subsystems, and an in-app JSON
  snapshot endpoint — so memory is observable continuously, not just during a
  profiling session.
- A **written, reproducible profiling methodology** (a runbook) covering both
  the API-server and worker roles, heap/alloc/goroutine/block/mutex profiles,
  base-diffing over time, and the off-heap accounting nuance.
- A **measured findings report**: where memory actually goes under
  `make bench-checks` load, whether anything leaks over a multi-hour soak, and a
  **prioritized backlog of remediation specs** (this spec measures and
  instruments; it does **not** ship the optimizations — each becomes its own
  follow-up so fixes are driven by data, not guesses).
- **Automated leak guards**: goroutine-leak tests on the worker and server
  lifecycle so regressions are caught in CI.

## Approach / decision

Two parallel tracks that feed each other:

- **Track A — Monitoring (mostly code/APIs):** make memory continuously
  observable. Low-risk, high-leverage, lands first because it also instruments
  the soak test in Track B.
- **Track B — Code & profiling analysis (mostly process):** use the Track-A
  surface plus pprof to find and attribute the hotspots and leaks, then audit the
  specific suspects below.

Reuse what exists: model the new metrics collector on
[`db_collector.go`](server/internal/prommetrics/db_collector.go), put the JSON
endpoint under the existing `/api/mgmt` group, drive load with
`make bench-checks`. Don't reinvent a profiler — extend the one we have.

---

## Track A — Monitoring instrumentation

### A1. Register Go runtime + process collectors (the quick win)

In `prommetrics.Register(...)` (the function called at
[`server.go:1021`](server/internal/app/server.go#L1021)), register:

```go
reg.MustRegister(collectors.NewGoCollector(
    collectors.WithGoCollectorRuntimeMetrics(collectors.MetricsAll),
))
reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
```

(`github.com/prometheus/client_golang/prometheus/collectors`.) This yields
`go_memstats_heap_inuse_bytes`, `go_memstats_heap_alloc_bytes`,
`go_memstats_stack_inuse_bytes`, `go_goroutines`, `go_gc_duration_seconds`,
`go_memstats_next_gc_bytes`, and `process_resident_memory_bytes` /
`process_virtual_memory_bytes`. The **RSS vs heap-inuse** pair is the headline
leak signal and the off-heap detector. Must be present on **both** the server and
worker `/metrics`.

### A2. Custom gauges for the suspect subsystems

A new collector (mirror [`db_collector.go`](server/internal/prommetrics/db_collector.go),
read sizes at scrape time — zero idle cost) exposing:

| Metric | Source | Why |
|---|---|---|
| `solidping_dek_cache_entries` | DEK `sync.Map` count, [`crypto/credentials/service.go:76`](server/internal/crypto/credentials/service.go#L76) | grows O(orgs), never evicted |
| `solidping_ratelimit_entries` | rate-limiter `entries` map, [`middleware/ratelimit.go:62`](server/internal/middleware/ratelimit.go#L62) | grows O(unique IPs); verify cleanup keeps up |
| `solidping_event_listeners` | local notifier listeners, [`notifier/local.go:13`](server/internal/notifier/local.go#L13) | channel-per-Listen; leak if not cleaned |
| `solidping_runtime_goroutines` | `runtime.NumGoroutine()` | cross-checks `go_goroutines`; cheap to also surface in the API |

These count things `go_memstats` can't attribute. Adding a `Len()`/count accessor
to each subsystem (rather than reaching into internals) keeps the collector clean.

### A3. Memory snapshot API endpoint (the "API" deliverable)

`GET /api/mgmt/memory` returning a JSON snapshot for humans and scripts:

```jsonc
{
  "data": {
    "runtime": {                      // from runtime.ReadMemStats + runtime.NumGoroutine
      "heapAllocBytes": 0, "heapInuseBytes": 0, "heapObjects": 0,
      "stackInuseBytes": 0, "sysBytes": 0, "numGoroutine": 0,
      "numGC": 0, "gcPauseTotalNs": 0, "nextGCBytes": 0, "gcCPUFraction": 0.0
    },
    "process": { "rssBytes": 0 },     // RSS so the off-heap gap is visible in one place
    "subsystems": {                   // the A2 sizes
      "dekCacheEntries": 0, "rateLimitEntries": 0, "eventListeners": 0
    },
    "build": { "cgoEnabled": false, "sqliteDriver": "modernc|mattn", "goVersion": "" }
  }
}
```

`build.cgoEnabled` / `sqliteDriver` answer the off-heap question (gap #6) at a
glance. **Auth: super-admin only** (`RequireSuperAdmin`, the `/system/*`
pattern) — memstats + subsystem cardinality are operationally sensitive, unlike
`health`/`version`. (The raw pprof surface stays on the localhost-bound profiler
server; this endpoint is the safe, structured, in-app view.) Decision is
adjustable, but default to gated.

### A4. Make block/mutex profiling actually work

Add `SP_PROFILER_BLOCK_RATE` (int, default 0) and `SP_PROFILER_MUTEX_FRACTION`
(int, default 0) to `ProfilerConfig`
([`config.go:475`](server/internal/config/config.go#L475)). When the profiler
starts and the values are > 0, call `runtime.SetBlockProfileRate(rate)` and
`runtime.SetMutexProfileFraction(frac)` in
[`profiler.go`](server/internal/profiler/profiler.go). Off by default (they have
runtime cost); opt-in for a profiling session. This unlocks `/debug/pprof/block`
and `/mutex`, useful when goroutines pile up on a channel/lock and hold memory.

### A5. Dashboards + alerts (process, not code in this repo)

Document Grafana panels (server + worker): `process_resident_memory_bytes`,
`go_memstats_heap_inuse_bytes`, `go_goroutines`, `go_gc_duration_seconds`, plus
the A2 gauges. Define alert rules: **sustained RSS growth** (e.g.
`deriv(process_resident_memory_bytes[1h]) > 0` held for hours),
**goroutine growth** without bound, and `go_goroutines` / `dek_cache_entries`
crossing sane ceilings. Ships as a JSON dashboard + rules in `wiki/` / docs, not
application code.

### A6. (Optional) GC levers documentation

Document `GOMEMLIMIT` (soft cap — valuable for memory-constrained self-hosted
boxes) and `GOGC` (throughput vs footprint) as operational knobs, with the
trade-offs. No code; a docs section so operators have a dial after Track B
quantifies the baseline.

---

## Track B — Code & profiling analysis

### B1. Establish the methodology runbook

Write `wiki/runbooks/memory-profiling.md` with copy-paste commands, covering
**both roles separately** (the server and a worker are different processes with
different memory shapes; in a combined `make dev` run, give each a distinct
`SP_PROFILER_LISTEN` port):

```bash
# Enable profiling
SP_PROFILER_ENABLED=true SP_PROFILER_LISTEN=localhost:6060 ./solidping serve
# In-use heap (what's live right now — the leak/steady-state view)
go tool pprof -inuse_space http://localhost:6060/debug/pprof/heap
# Cumulative allocations (what churns — the GC-pressure view)
go tool pprof -alloc_space http://localhost:6060/debug/pprof/heap
# Goroutines (count + stacks; leak detection)
go tool pprof http://localhost:6060/debug/pprof/goroutine
curl -s 'http://localhost:6060/debug/pprof/goroutine?debug=2' | less
# Flamegraph UI
go tool pprof -http=: <profile>
```

Document **base-diffing** (the core leak technique): snapshot
`heap.0`, wait, snapshot `heap.1`, then
`go tool pprof -base heap.0.pb.gz -inuse_space heap.1.pb.gz` to see *what grew*.
Document the off-heap rule: if `process_resident_memory_bytes` ≫
`go_memstats_heap_inuse_bytes + stack + …` and `cgoEnabled=true`, the gap is C
allocations (SQLite/mattn) invisible to pprof — investigate via OS tooling, not
Go profiles.

### B2. Baseline under load

Run `make bench-checks BENCH_CHECKS=<small,med,large>` (e.g. 50 / 500 / 5000) for
SQLite **and** Postgres. For each, capture inuse + alloc heap and a goroutine
profile at steady state; record `process_resident_memory_bytes` and
`go_goroutines`. Produce a **memory-vs-check-count** table per backend — this is
the "what scales" answer and the input to capacity guidance.

### B3. Soak / leak detection

Drive steady `bench-checks` load for several hours. Sample `/api/mgmt/memory`
(A3) and `/metrics` every 5 min; plot RSS, heap-inuse, goroutines, and the A2
gauges over time. A rising inuse_space base-diff or unbounded goroutine/gauge
growth = leak → attribute via `pprof -base`. Flat trend = no leak at that scale.

### B4. Allocation-hotspot benchmarks + escape analysis

For the hot paths, add `-benchmem` benchmarks and read escape analysis:

```bash
go test -bench=. -benchmem -memprofile=mem.out ./internal/checkers/checkhttp/
go build -gcflags='-m' ./internal/checkers/checkhttp/ 2>&1 | rg 'escapes to heap'
```

Targets: check execution, the HTTP checker body read, result
serialization/JSONMap handling, and aggregation. The goal is allocs/op on the
per-execution path (multiplied by thousands/min, small wins compound).

### B5. Static analysis

Enable/confirm the memory-relevant linters in `.golangci.yml`: **`bodyclose`**
(leaked HTTP bodies → leaked connections/buffers), **`prealloc`** (already used
per `server/CLAUDE.md`), **`makezero`**. Run `staticcheck`. Audit every
`io.ReadAll` / `LimitReader` site for an enforced bound. (Per repo policy: fix
findings in code; never relax the linter config.)

### B6. Leak-guard tests (code, lands in CI)

Add `go.uber.org/goleak`: `goleak.VerifyTestMain(m)` in the checkworker and app
packages, and explicit assertions that `worker.Stop()` / server shutdown leave no
leaked goroutines. Catches express-runner ([`worker.go:327`](server/internal/checkworker/worker.go#L327))
and notifier-listener leaks as regressions.

### B7. Targeted subsystem audits

Read each suspect below with the profile data in hand and write a finding
(confirmed / not-an-issue / fix-spec-filed). **Findings only — fixes are
follow-up specs.**

---

## Hypotheses to investigate (prioritized)

Grounded in a first-pass code read; each is a B7 audit item, ranked by expected
impact for a busy multi-tenant instance.

| # | Suspect | Location | Hypothesis / risk |
|---|---|---|---|
| 1 | **New `http.Client` per check** | [`checkhttp/checker.go:249`](server/internal/checkers/checkhttp/checker.go#L249) | No transport reuse → per-execution alloc churn + idle-conn pools that don't amortize. Highest-frequency path. |
| 2 | **DEK cache never evicts** | [`crypto/credentials/service.go:76`](server/internal/crypto/credentials/service.go#L76) | `sync.Map` grows O(orgs) for the process lifetime. Bounded by org count, but unbounded in time. |
| 3 | **Aggregation loads full result set** | [`jobs/jobtypes/job_aggregation.go:149`](server/internal/jobs/jobtypes/job_aggregation.go#L149) | `ListResults` with no pagination → tens of MB transient spikes for high-frequency checks; coincides with GC pressure. |
| 4 | **Postgres pool unbounded** | [`db/postgres/postgres.go:84`](server/internal/db/postgres/postgres.go#L84) | No `SetMaxOpenConns` → connections (and their buffers) grow with concurrency under load. |
| 5 | **10 MB body buffered per HTTP check** | [`checkhttp/checker.go:63`](server/internal/checkers/checkhttp/checker.go#L63) | `LimitReader`+`ReadAll` holds up to 10 MB × concurrent checks when body matching is on. Confirm it's only buffered when needed. |
| 6 | **Rate-limiter IP map** | [`middleware/ratelimit.go:62`](server/internal/middleware/ratelimit.go#L62) | O(unique IPs) with async `cleanupLoop` — verify cleanup keeps pace under a wide client base / scan. |
| 7 | **Event-listener channels** | [`notifier/local.go:13`](server/internal/notifier/local.go#L13) | channel-per-`Listen`; leak if listeners aren't deregistered (overlaps B6). |
| 8 | **Bun prepared-statement cache** | Bun ORM usage | grows O(unique query shapes); confirm it's bounded in practice. |
| 9 | **Prometheus label cardinality** | [`prommetrics/metrics.go`](server/internal/prommetrics/metrics.go) | per-check×region×org series (`check_up`, `check_status_streak`) live in-process O(checks×regions×orgs); meta-cost of the metrics themselves. |

## Out of scope

- **The actual optimizations/fixes.** This spec instruments, measures, and
  reports. Each confirmed finding becomes its own remediation spec (e.g. "reuse
  HTTP transport across checks", "bound the DEK cache", "paginate aggregation
  reads") so fixes are data-driven and independently reviewable.
- **Frontend / browser memory** (dash0, status0) — a separate concern.
- **Changing worker concurrency or lease semantics** — only measured, not
  altered.
- **A continuous-profiling stack** (Pyroscope / Grafana Alloy / `parca`) — note
  it as a possible follow-up in the runbook; not set up here.

## Testing / verification

- **A1/A2:** `curl localhost:4000/metrics | rg 'go_memstats_heap_inuse_bytes|process_resident_memory_bytes|go_goroutines|solidping_dek_cache_entries'`
  returns series on **both** server and worker. Unit-test the custom collector
  (mirror `db_collector` tests) — `Describe`/`Collect` emit the expected metrics.
- **A3:** table-driven handler test: shape + `{data}` wrapping + camelCase;
  **auth matrix** (viewer/user/org-admin → 403; super-admin → 200);
  `build.cgoEnabled`/`sqliteDriver` reflect the build. Manual:
  `curl -H "Authorization: Bearer $TOKEN" localhost:4000/api/mgmt/memory | jq`.
- **A4:** with `SP_PROFILER_BLOCK_RATE=1`, `go tool pprof
  http://localhost:6060/debug/pprof/block` returns a non-empty profile; with the
  default 0 it's empty (no regression to existing pprof endpoints).
- **B6:** goleak tests pass in CI and **fail** when a deliberately-leaked
  goroutine is introduced (prove the guard works).
- **B (process):** the runbook is reproducible end-to-end by someone else; the
  baseline table (B2) and soak result (B3) are recorded in the findings report;
  every hypothesis has a written verdict.
- Standard gates: `make lint`, `make test` green.

## Deliverables

1. **Code:** A1 collectors, A2 subsystem collector + `Len()` accessors, A3
   `/api/mgmt/memory` endpoint, A4 block/mutex config, B6 goleak tests.
2. **Process docs:** `wiki/runbooks/memory-profiling.md` (B1), Grafana
   dashboard JSON + alert rules (A5), GC-levers note (A6).
3. **Findings report** (`wiki/` or this spec's done-companion): baseline table
   (B2), soak result (B3), per-hypothesis verdicts (B7), and a **prioritized list
   of remediation specs** to file.

## Implementation plan

Phased so the monitoring surface (which the soak test depends on) lands first.

1. **A1 + A2 + A4** — collectors and profiler config. Smallest, highest-leverage,
   unblocks everything. Verify `/metrics` and pprof.
2. **A3** — `/api/mgmt/memory` endpoint + super-admin gate + tests.
3. **B6** — goleak tests (independent; can land in parallel with 1–2).
4. **B1** — write the profiling runbook.
5. **B2 + B3** — run baseline + soak with `make bench-checks`; capture profiles
   and trends using the now-live metrics/endpoint.
6. **B4 + B5** — allocation benchmarks, escape analysis, static-analysis pass.
7. **B7** — audit the nine hypotheses against the data; write the findings
   report and file the prioritized remediation specs.
8. **A5 + A6** — dashboards/alerts/GC-levers docs, informed by the measured
   baseline.

## Implementation Plan (subagent — concrete file mapping)

Maps each **in-repo** deliverable to concrete files. Integrates with the existing
registry/profiler rather than building new ones. The single `app.Server` struct
hosts metrics registration + profiler regardless of `Node.Role`, so A1/A2/A4 are
present on the API server and the worker (`role=jobs`) alike; the per-worker
channel collector ([`checkworker/metrics.go`](server/internal/checkworker/metrics.go))
confirms workers register into `prometheus.DefaultRegisterer`.

**A1 — Go + process collectors** (`server/internal/prommetrics/runtime_collector.go`):
`Register(...)` also registers `collectors.NewGoCollector(WithGoCollectorRuntimeMetrics(MetricsAll))`
and `collectors.NewProcessCollector(...)`. Gated by `Prometheus.Enabled` (same
call site, [`server.go:1022`](server/internal/app/server.go#L1022)). Tested by
extending `metrics_test.go` to assert `go_memstats_heap_inuse_bytes`,
`process_*`, `go_goroutines` appear after `Register`.

**A2 — subsystem collector** (`server/internal/prommetrics/subsystem_collector.go`):
a scrape-time `prometheus.Collector` (mirror `db_collector.go`) reading sizes via
injected closures (avoids prommetrics→middleware/credentials/notifier import
cycles). New `Len()`/count accessors:
- `credentials.Service.DEKCacheLen()` (range `dekCache sync.Map`) — [`service.go:80`](server/internal/crypto/credentials/service.go#L80)
- `middleware.RateLimiter.EntryCount()` (range `entries sync.Map`) — [`ratelimit.go:62`](server/internal/middleware/ratelimit.go#L62)
- `notifier.LocalEventNotifier.ListenerCount()` + `PgEventNotifier.ListenerCount()`, surfaced through a `ListenerCounter` interface checked by the collector — [`local.go:13`](server/internal/notifier/local.go#L13)
Metrics: `solidping_dek_cache_entries`, `solidping_ratelimit_entries`,
`solidping_event_listeners`, `solidping_runtime_goroutines`. Registered in
`server.go` after `prommetrics.Register`, wired to the live services/rate-limiter.
Unit-tested with stub closures.

**A3 — `GET /api/mgmt/memory`** (`server/internal/app/memory_handler.go` +
route in `server.go`): super-admin gated via its own
`mainGroup.NewGroup("/api/mgmt").Use(RequireAuth, RequireSuperAdmin)` sub-group
(the public `/api/mgmt/{health,version,limits,report}` stay open). Payload per
spec: `{data:{runtime,process,subsystems,build}}`, camelCase. `runtime` from
`runtime.ReadMemStats`+`NumGoroutine`; `process.rssBytes` from the process
collector's RSS (read via `gopsutil`/`/proc` helper already pulled by the process
collector — fall back to 0 if unavailable); `build` from a new `buildinfo` pkg.
Table-driven handler test incl. auth matrix (viewer/user/org-admin→403,
super-admin→200) + shape/camelCase.

**buildinfo** (`server/internal/buildinfo/`): `cgo.go` (`//go:build cgo`) and
`nocgo.go` (`//go:build !cgo`) set `CGOEnabled` + `SQLiteDriver()` ("mattn" vs
"modernc", matching `sqliteshim`'s CGO selection); `GoVersion()` wraps
`runtime.Version()`. Answers off-heap gap #6 at a glance.

**A4 — block/mutex profiling** ([`config.go`](server/internal/config/config.go#L182)
+ [`profiler.go`](server/internal/profiler/profiler.go)): add
`BlockRate`/`MutexFraction` to `ProfilerConfig` (koanf `block_rate` /
`mutex_fraction`) with a new `applyProfilerEnv` (multi-word koanf-quirk reader,
`SP_PROFILER_BLOCK_RATE` / `SP_PROFILER_MUTEX_FRACTION`). `profiler.Start` calls
`runtime.SetBlockProfileRate` / `SetMutexProfileFraction` only when > 0. Off by
default. Unit test on the config reader + a profiler test that the rates are set.

**B6 — goleak guards** (`go.uber.org/goleak`): `TestMain` with
`goleak.VerifyTestMain(m)` in `checkworker` and a worker-lifecycle test asserting
`Run(ctx)` returns and leaves no leaked goroutines after `cancel()`. Catches
express-runner/notifier-listener leaks.

**B1 — runbook** (`wiki/runbooks/memory-profiling.md`): copy-paste pprof commands
for both roles, base-diffing, off-heap rule, the new `/api/mgmt/memory` +
`/metrics` signals, `bench-checks` load driving.

**B4 — allocation benchmarks** (`server/internal/checkers/checkhttp/checker_bench_test.go`
or in-package `_test.go`): `Benchmark...` with `b.ReportAllocs()` over the HTTP
checker hot path (body read / result handling) — the per-execution allocs/op
signal. Compile + 1-iteration run in QA; not run to completion.

**A5/A6 — docs only** (`wiki/runbooks/memory-profiling.md` appendices):
Grafana panel/alert guidance and `GOMEMLIMIT`/`GOGC` levers. The Grafana
JSON/alert-rules *files* and a continuous-profiling stack are **process, not code
here** — documented, not shipped as deployable config.

### Treated as out-of-scope / one-time analysis (not committed code)
- **B2 baseline / B3 soak** — require sustained multi-backend + multi-hour load I
  cannot run to completion in this environment. I ship the runbook + benchmark
  code (the reproducible artifacts) and note the measurement runs as a one-time
  activity for an operator. Any short-run numbers are illustrative only.
- **B5 static-analysis remediation** — `.golangci.yml` already carries the
  memory-relevant linters per `server/CLAUDE.md`; I confirm/document them in the
  runbook and rely on `make lint-back` in QA. I do **not** relax the config
  (repo policy) and do not file the per-finding fixes here.
- **B7 nine-hypothesis verdicts** — these are an analysis activity feeding
  follow-up remediation specs; the runbook documents how to run them. The actual
  measured verdicts/findings report + the remediation-spec backlog are the
  one-time deliverable, not committable instrumentation. A skeleton findings
  section is included in the runbook for an operator to fill from a real run.
- **A5 Grafana dashboard JSON + alert rules**, **Pyroscope/Parca continuous
  profiling**, and **all optimizations/fixes** — explicitly out of scope per the
  spec's *Out of scope* section.

## Files referenced

- [`server/internal/profiler/profiler.go`](server/internal/profiler/profiler.go)
  — pprof server; add block/mutex rate (A4).
- [`server/internal/config/config.go:475`](server/internal/config/config.go#L475)
  — `ProfilerConfig` (A4); `:77` OTel, `:108` Prometheus.
- [`server/internal/prommetrics/metrics.go`](server/internal/prommetrics/metrics.go)
  — `Register(...)` (A1); existing app metrics (hypothesis #9).
- [`server/internal/prommetrics/db_collector.go`](server/internal/prommetrics/db_collector.go)
  — the scrape-time collector pattern to mirror (A2).
- [`server/internal/app/server.go:1014`](server/internal/app/server.go#L1014)
  — `/api/mgmt` group (A3); `:1021` metrics registration (A1); `:334`/`:1663`
  profiler wiring.
- [`server/internal/checkworker/worker.go:327`](server/internal/checkworker/worker.go#L327)
  — express runner (B6, hypothesis #7).
- [`server/internal/checkers/checkhttp/checker.go:63`](server/internal/checkers/checkhttp/checker.go#L63)
  — body limit (#5); `:249` per-check client (#1).
- [`server/internal/crypto/credentials/service.go:76`](server/internal/crypto/credentials/service.go#L76)
  — DEK cache (#2, A2).
- [`server/internal/middleware/ratelimit.go:62`](server/internal/middleware/ratelimit.go#L62)
  — IP map (#6, A2).
- [`server/internal/notifier/local.go:13`](server/internal/notifier/local.go#L13)
  — listener channels (#7, A2).
- [`server/internal/db/postgres/postgres.go:84`](server/internal/db/postgres/postgres.go#L84)
  — pool sizing (#4); [`db/sqlite/sqlite.go:119`](server/internal/db/sqlite/sqlite.go#L119)
  — driver (off-heap, gap #6).
- [`server/internal/jobs/jobtypes/job_aggregation.go:149`](server/internal/jobs/jobtypes/job_aggregation.go#L149)
  — full result-set load (#3).
- [`server/internal/stats/processingStats.go`](server/internal/stats/processingStats.go)
  — existing EWMA stats (optional surface in A3).
- [`Makefile:181`](Makefile#L181) — `bench-checks` load harness (B2/B3).
