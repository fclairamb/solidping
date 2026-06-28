# Runbook: memory profiling & leak detection

How to answer three questions for the **API server** and the **worker** —
separately, since they are different processes with different memory shapes:

1. **Where does memory go?** (steady-state heap composition, off-heap, goroutines)
2. **Does it grow without bound?** (leaks over time under steady load)
3. **What scales with tenants/checks/workers?** (the structures that OOM at scale)

The standing surfaces this relies on (all shipped):

- **Prometheus** (`/metrics`, gated by `SP_PROMETHEUS_ENABLED`, default on) — Go
  runtime + process collectors (`go_memstats_*`, `process_resident_memory_bytes`,
  `go_goroutines`, `go_gc_*`) plus the subsystem gauges
  `solidping_dek_cache_entries`, `solidping_ratelimit_entries`,
  `solidping_event_listeners`, `solidping_runtime_goroutines`.
- **`GET /api/mgmt/memory`** (super-admin) — a JSON snapshot of memstats, RSS,
  the subsystem sizes, and the build's cgo / SQLite-driver facts.
- **pprof** (`SP_PROFILER_ENABLED=true`, listen `SP_PROFILER_LISTEN`,
  default `localhost:6060`) — `/debug/pprof/{heap,goroutine,allocs,profile,...}`,
  plus `block`/`mutex` once their rates are enabled (see below).

> The two roles run as one process in `make dev` (`role=all`). To profile them as
> separate processes, run two nodes (`SP_NODE_ROLE=api` and `SP_NODE_ROLE=jobs`)
> and give each its own `SP_PROFILER_LISTEN` port. Note: `/metrics` is only
> *served* on a node that runs the API (`role=all` or `role=api`); a `jobs`-only
> node still registers the collectors but does not expose the scrape endpoint, so
> co-locate an API or scrape the combined `role=all` process.

---

## 1. Enable profiling

```bash
# API server (or combined role=all)
SP_PROFILER_ENABLED=true SP_PROFILER_LISTEN=localhost:6060 ./solidping serve

# A separate worker on its own pprof port
SP_NODE_ROLE=jobs SP_PROFILER_ENABLED=true SP_PROFILER_LISTEN=localhost:6061 ./solidping serve
```

Optional, opt-in (runtime cost — only during a profiling session):

```bash
# Populate /debug/pprof/block and /debug/pprof/mutex (empty otherwise)
SP_PROFILER_BLOCK_RATE=1 SP_PROFILER_MUTEX_FRACTION=1 ...
```

## 2. The three profiles

```bash
# In-use heap — what is LIVE right now (the leak / steady-state view)
go tool pprof -inuse_space http://localhost:6060/debug/pprof/heap

# Cumulative allocations — what CHURNS (the GC-pressure view)
go tool pprof -alloc_space http://localhost:6060/debug/pprof/heap

# Goroutines — count + stacks (leak detection)
go tool pprof http://localhost:6060/debug/pprof/goroutine
curl -s 'http://localhost:6060/debug/pprof/goroutine?debug=2' | less

# Contention (only if BLOCK/MUTEX rates were enabled)
go tool pprof http://localhost:6060/debug/pprof/block
go tool pprof http://localhost:6060/debug/pprof/mutex

# Flamegraph UI for any saved profile
go tool pprof -http=: <profile>
```

Inside `pprof`: `top`, `top -cum`, `list <func>`, `web`, `peek <func>`.

## 3. Base-diffing — the core leak technique

Snapshot, wait under steady load, snapshot again, diff to see *what grew*:

```bash
curl -s http://localhost:6060/debug/pprof/heap > heap.0.pb.gz
# ... keep load running for N minutes ...
curl -s http://localhost:6060/debug/pprof/heap > heap.1.pb.gz

go tool pprof -base heap.0.pb.gz -inuse_space heap.1.pb.gz   # heap growth
```

Do the same with the goroutine profile to find goroutine leaks. A flat diff = no
leak at that scale; a rising `inuse_space` base or unbounded goroutine/gauge
growth = a leak → attribute it from the diff's `top`.

## 4. The off-heap rule (do not misattribute the RSS gap)

`GET /api/mgmt/memory` reports `build.cgoEnabled` and `build.sqliteDriver`:

- **cgo off → `modernc`** (pure-Go SQLite): allocations are **on-heap**, so they
  show up in pprof heap profiles. The `RSS − go_heap` gap should be small.
- **cgo on → `mattn`** (C SQLite): allocations are **off-heap**, invisible to
  pprof. If `process_resident_memory_bytes` ≫
  `go_memstats_heap_inuse_bytes + stack + …`, the gap is C allocations —
  investigate with OS tooling (`pmap`, `/proc/<pid>/smaps`, jemalloc/heaptrack),
  **not** Go profiles.

Establish which driver the build uses **before** analysis, or every conclusion
about the RSS gap is suspect.

---

## 5. Baseline under load (B2)

Drive load with the bundled harness for SQLite **and** Postgres at a few check
counts, capture profiles + metrics at steady state, and fill the table below.

```bash
make bench-checks BENCH_CHECKS=50      # then 500, then 5000
# (make bench-checks runs both bench-checks-sqlite and bench-checks-postgres)
```

For each (backend × check-count), at steady state record:

- `process_resident_memory_bytes`, `go_memstats_heap_inuse_bytes`,
  `go_goroutines` (from `/metrics` or `/api/mgmt/memory`)
- an `-inuse_space` heap profile and a goroutine profile

| Backend  | Checks | RSS | heap_inuse | goroutines | top inuse_space consumer |
|----------|-------:|----:|-----------:|-----------:|--------------------------|
| sqlite   |     50 |     |            |            |                          |
| sqlite   |    500 |     |            |            |                          |
| sqlite   |   5000 |     |            |            |                          |
| postgres |     50 |     |            |            |                          |
| postgres |    500 |     |            |            |                          |
| postgres |   5000 |     |            |            |                          |

The RSS / heap slope vs check-count is the **"what scales"** answer and the input
to capacity guidance.

> This baseline is a **one-time measurement activity**, not committed data. Run
> it on a representative box and paste the results into the findings section
> below (or a dated companion doc).

## 6. Soak / leak detection (B3)

Drive steady `bench-checks` load for several hours. Sample `/api/mgmt/memory` and
`/metrics` every 5 min; plot RSS, heap-inuse, goroutines, and the subsystem
gauges over time.

```bash
while true; do
  curl -s -H "Authorization: Bearer $TOKEN" localhost:4000/api/mgmt/memory \
    | jq -c '{t: now, rss: .data.process.rssBytes, heap: .data.runtime.heapInuseBytes,
              gor: .data.runtime.numGoroutine, dek: .data.subsystems.dekCacheEntries,
              rl: .data.subsystems.rateLimitEntries, lst: .data.subsystems.eventListeners}'
  sleep 300
done | tee soak.jsonl
```

A rising `inuse_space` base-diff or unbounded goroutine/gauge growth = leak →
attribute via `pprof -base`. Flat = no leak at that scale. Also a **one-time
activity**.

## 7. Allocation hotspots & escape analysis (B4/B5)

```bash
# Per-execution allocs/op on the HTTP checker hot path (benchmarks ship in-repo)
go test -run=XXX -bench='BenchmarkHTTPCheckerExecute' -benchmem ./internal/checkers/checkhttp/

# What escapes to the heap on a hot path
go build -gcflags='-m' ./internal/checkers/checkhttp/ 2>&1 | rg 'escapes to heap'
```

Static analysis (per repo policy — fix findings in code, never relax the config):
`make lint-back` runs the memory-relevant linters (`bodyclose`, `prealloc`,
`makezero`); `staticcheck` is part of the suite. Audit every `io.ReadAll` /
`LimitReader` site for an enforced bound.

---

## 8. Findings (B7) — fill from a real baseline/soak run

> Skeleton for an operator to complete; verdicts feed **separate remediation
> specs** (this work instruments and measures; it does not ship the fixes).

| # | Suspect | Verdict (confirmed / not-an-issue / fix-spec-filed) | Notes |
|---|---------|------------------------------------------------------|-------|
| 1 | New `http.Client` per check (`checkhttp/checker.go`) | _tbd_ | baseline allocs/op: `BenchmarkHTTPCheckerExecute` |
| 2 | DEK cache never evicts (`crypto/credentials/service.go`) | _tbd_ | watch `solidping_dek_cache_entries` over soak |
| 3 | Aggregation loads full result set (`jobs/jobtypes/job_aggregation.go`) | _tbd_ | look for transient `alloc_space` spikes |
| 4 | Postgres pool unbounded (`db/postgres/postgres.go`) | _tbd_ | `solidping_db_pool_open_connections` under load |
| 5 | 10 MB body buffered per HTTP check (`checkhttp/checker.go`) | _tbd_ | `BenchmarkHTTPCheckerExecuteBodyMatch` bytes/op |
| 6 | Rate-limiter IP map (`middleware/ratelimit.go`) | _tbd_ | `solidping_ratelimit_entries` vs cleanup cadence |
| 7 | Event-listener channels (`notifier/local.go`) | _tbd_ | `solidping_event_listeners` + goleak guard |
| 8 | Bun prepared-statement cache | _tbd_ | heap base-diff for query-shape growth |
| 9 | Prometheus label cardinality (`prommetrics/metrics.go`) | _tbd_ | series count = O(checks×regions×orgs) |

**Prioritized remediation backlog** (file each as its own spec once confirmed):
record here, ranked by measured impact.

---

## Appendix A — dashboards & alerts (A5, process not code)

Build Grafana panels per role from these series (no JSON shipped in-repo — define
them in your Grafana / IaC of choice):

- `process_resident_memory_bytes`, `go_memstats_heap_inuse_bytes`,
  `go_goroutines`, `go_gc_duration_seconds`
- the subsystem gauges (`solidping_dek_cache_entries`,
  `solidping_ratelimit_entries`, `solidping_event_listeners`)

Suggested alert rules:

- **Sustained RSS growth** — `deriv(process_resident_memory_bytes[1h]) > 0` held
  for several hours.
- **Unbounded goroutines** — `go_goroutines` trending up with no plateau, or
  crossing a sane ceiling.
- **Subsystem ceilings** — `solidping_dek_cache_entries` /
  `solidping_ratelimit_entries` crossing instance-sized thresholds.

A continuous-profiling stack (Pyroscope / Grafana Alloy / Parca) is a possible
follow-up; it is **not** set up here.

## Appendix B — GC levers (A6, operational knobs)

- **`GOMEMLIMIT`** — a soft memory cap. Valuable on memory-constrained
  self-hosted boxes: the GC works harder as RSS approaches the limit, trading CPU
  to avoid the OOM killer. Set it a bit below the container/host limit (e.g.
  `GOMEMLIMIT=400MiB`). It is a *soft* limit — Go will still exceed it rather than
  stall forever.
- **`GOGC`** — the heap-growth target (default 100 = GC when heap doubles).
  Lower (e.g. `50`) trims footprint at the cost of more frequent GC; higher
  (e.g. `200`) favors throughput at the cost of a larger heap. Quantify the
  baseline with section 5 before turning this dial.

Both are environment variables; no application code is involved.
