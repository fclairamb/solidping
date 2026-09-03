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
- **`GET /api/mgmt/memory`** (super-admin) — a JSON snapshot of memstats and
  the `runtime/metrics` memory classes, the `/proc` RSS breakdown (anon / file /
  shmem split, `VmHWM`, threads, `smaps_rollup` Pss), the container's own cgroup
  accounting, the derived off-heap gap, the subsystem sizes, and the build's
  cgo / SQLite-driver facts. `?gc=1` returns the **floor** instead of the steady
  state (`runtime.GC()` + `FreeOSMemory()` first); the payload's `sample.mode`
  says which you got. Same numbers on `/metrics` as `solidping_process_rss_*`,
  `solidping_process_smaps_*`, `solidping_cgroup_memory_*` and
  `solidping_process_offheap_bytes`.
- **`make bench-memory`** (`server/cmd/membench`) — the repeatable measurement:
  boots a fresh server per repetition, warms up, samples, repeats, and reports
  the **inter-run spread** that decides whether a delta means anything. See §5.
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

## 4. Which "RSS"? — the four numbers, and the one that matters

Four different quantities get called "memory", and picking the wrong one is how
a memory investigation wastes a week:

| Number | Where | What it is | Can it OOM you? |
|---|---|---|---|
| `kubectl top` working set | kubelet, from the cgroup | `memory.current` − inactive file pages. **Includes** actively mapped pages of the 240 MB binary. | Indirectly — it is what the kubelet evicts on |
| `process_resident_memory_bytes` (`rssBytes`) | `/proc/self/stat` | anon + file + shmem, in one number, unsplit | Only its anon part |
| Go `Sys` / heap-inuse | the runtime | what the **runtime** took from the OS. Blind to a cgo arena, to OS thread stacks it did not create, and to the binary's text/rodata | Only partly |
| **cgroup `anon + kernel`** | `memory.stat` | what the kernel **cannot reclaim** under pressure | **Yes — this is the OOM decision** |

`GET /api/mgmt/memory` reports all four, so they never have to be guessed at:

- `process.status.rssAnonBytes` / `rssFileBytes` — the split `rssBytes` hides.
  File-backed pages are clean and reclaimable; they show up in `kubectl top` and
  can never cause an OOM kill.
- `process.smaps.sharedCleanBytes` — dominated by the binary's mapped
  text/rodata. **This is where a binary-size change shows up, and nowhere else.**
- `cgroup.unreclaimableBytes` — `anon + kernel`. The primary metric of
  `make bench-memory`, and the number to put in a capacity argument.
- `derived.offHeapBytes` = `rssAnon − (classes.total − heapReleased)` — resident
  anonymous memory the Go runtime does not account for. Signed on purpose: a
  small negative value means the runtime holds address space that is not
  resident, which is information rather than an error, and
  `derived.offHeapKnown` is false (not zero) where `/proc` is unavailable.

### The off-heap rule, corrected

The rule used to read "cgo on → mattn → the RSS gap is C allocations". **That
was wrong, and the correction matters more than the rule.**

`uptrace/bun/driver/sqliteshim` gates its modernc file on `!cgosqlite` **and a
platform list** — not on `!cgo`. Enabling cgo therefore never selected mattn;
only `-tags cgosqlite` does. Every SolidPing build, released images included,
has been running the **pure-Go modernc driver**, while
`buildinfo.SQLiteDriver()` cheerfully reported `mattn` whenever cgo was on.

Measured, not deduced: swapping to a direct mattn import made
`TestSchemaVanishesWhenDatabaseFileIsDeleted` fail (mattn and modernc recreate a
deleted database file differently), and restoring sqliteshim's exact build tags
made it pass again. `internal/db/sqlitedriver` now copies those tags verbatim and
links exactly one driver; `build.sqliteDriver` in the snapshot reads that
driver's own constant instead of inferring anything.

So, today:

- **`build.sqliteDriver == "modernc"`** (the shipped build): SQLite allocations
  are **on the Go heap** and pprof sees them. A large `derived.offHeapBytes` is
  *not* SQLite — look at OS thread stacks, `RssFile`, and the runtime's own
  metadata classes before blaming a C allocator.
- **`build.sqliteDriver == "mattn"`** (only with `-tags cgosqlite`): allocations
  are off-heap and invisible to pprof; investigate with `pmap` / `smaps`, not Go
  profiles.

Read `build.sqliteDriver` from the endpoint before drawing any conclusion about
the gap. Do not infer it from cgo — that inference is what put `_tbd_` findings
in this runbook for three months.

---

## 5. The baseline measurement (B2) — `make bench-memory`

A single snapshot is not a measurement. Three seconds after boot a local process
reads `heapAlloc 72 MB, RSS 144 MB`; ten minutes later the same process reads
very differently, and neither number changed because anything was fixed. So the
baseline is produced by a harness with a protocol, not by curl.

```bash
# Authoritative: the shipped image shape, real cgroup v2 accounting.
make bench-memory-image                     # build a Linux image from THIS tree
make bench-memory BENCH_MEM_MODE=docker \
     BENCH_MEM_SCENARIOS=idle-all-sqlite,checks-500,docs-crawl \
     BENCH_MEM_LABEL=baseline

# Fast iteration; explicitly NOT authoritative (no cgroup, host GOMAXPROCS).
make bench-memory BENCH_MEM_SCENARIOS=idle-all-sqlite

# Did my change do anything?
make bench-memory BENCH_MEM_MODE=docker BENCH_MEM_LABEL=candidate \
     BENCH_MEM_COMPARE=bench-results/memory-baseline.json
```

A third mode samples a server you did not start — a `make dev` process, a
`kubectl port-forward`ed pod — and starts and stops nothing:

```bash
./bin/membench -mode attach -base-url http://localhost:4000 \
     -scenarios idle-all-sqlite -reps 3 -label prod-api
```

Its reports are labelled `attach`, because nothing about the workload or the
warm-up was controlled by the harness.

**Protocol** (all overridable): boot a *fresh* server per repetition — reusing
one carries its page cache, heap and GC phase into the next run — warm up
`BENCH_MEM_WARMUP` (60 s), sample `/api/mgmt/memory` every `BENCH_MEM_INTERVAL`
(5 s) for `BENCH_MEM_DURATION` (5 min), `BENCH_MEM_REPS` (3) times. Output is a
markdown table and a JSON file, so two runs diff as text *and* mechanically.

**The primary metric is `cgroupUnreclaimableBytes` (cgroup `anon + kernel`)** —
what the OOM killer cannot reclaim. `pssBytes` and `rssFileBytes` are reported
**separately and never added to it**: they move with binary size and with which
pages a request touched, so folding them in would let a docs crawl look like a
heap regression. `cgroupPeakBytes` is the peak-signature metric — a transient
burst shows up there and nowhere else.

**The spread is the point.** Across the K repetitions the harness reports
max−min of the per-run medians. `--compare` refuses to call any delta smaller
than that spread a result, and refuses a verdict entirely when fewer than two
repetitions leave the spread unknown. If a candidate's claimed win is inside the
noise floor, it has not been shown to do anything — that is a rejection, and it
gets recorded in §9 with its numbers so nobody re-tries it blind.

### Scenarios

`idle-all-sqlite` (every role, no traffic) · `idle-api-sqlite` ·
`idle-api-checks-sqlite` · `idle-api-postgres` (local-only: embedded Postgres
downloads its own distribution) · `checks-200/500/1000` ·
`login-burst` (argon2id peak — a **security** parameter, measured, never
lowered) · `docs-crawl` (every path in `/docs/sitemap.xml`) · `dash0-reload`.

> A **checks-only** node cannot be sampled from inside: `serveAPIOrWait` starts
> no HTTP server for it, so there is no `/api/mgmt/memory` to read.
> `idle-api-checks-sqlite` is the closest in-process approximation, and its delta
> against `idle-all-sqlite` isolates the **jobs scheduler**, not the API surface.
> Measuring a true checks-only worker needs an external sampler (the host's
> cgroup files, or `kubectl top`).

### Measured baseline

<<BASELINE_TABLE>>

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

## 8. Findings (B7)

Verdicts feed **separate remediation specs** unless the fix was small enough to
ship with its measurement. Every row says how it was established, because
"confirmed" from a code read and "confirmed" from a bench are different claims.

| # | Suspect | Verdict | Evidence |
|---|---------|---------|----------|
| 1 | New `http.Client` per check (`checkhttp/checker.go`) | **not-an-issue on the common path** | `checkerdef.BuildHTTPTransport` returns **nil** unless a custom dialer, `verifySsl:false`, or an explicit IP family is in play, so `client.Transport` stays nil and net/http uses the shared `DefaultTransport` — one process-wide connection pool, not one per check. The `http.Client` struct itself is a handful of pointers. It *is* a fresh transport (and therefore an unshared pool) on the pinned-family / skip-verify / deported-dialer paths; that is the case worth a follow-up, not the default one. Established by code inspection, not by bench. |
| 2 | DEK cache never evicts (`crypto/credentials/service.go`) | **open — not measured** | Needs the 30-min soak of §6; `solidping_dek_cache_entries` is instrumented and O(orgs), so a per-org SaaS instance is where it would bite. |
| 3 | Aggregation loads full result set (`jobs/jobtypes/job_aggregation.go`) | **open — not measured** | Transient `alloc_space` spike; the bench scenarios are steady-state and would not surface it. |
| 4 | Postgres pool unbounded (`db/postgres/postgres.go`) | **fixed** | Bounded pool (defaults 25 open / 10 idle / 1 h lifetime, `SP_DB_MAX_OPEN_CONNS`). |
| 5 | 10 MB body buffered per HTTP check (`checkhttp/checker.go`) | **conditional, bounded** | The `io.ReadAll(io.LimitReader(body, 10 MB))` runs **only** when the check has body assertions or `captureFailureResponse`. It is not a per-check cost. When it does run, `io.ReadAll`'s geometric growth touches ~2× the final size transiently, per concurrent check. A streaming matcher would remove that peak. Established by code inspection; not measured under `checks-1000`. |
| 6 | Rate-limiter IP map (`middleware/ratelimit.go`) | **open — not measured** | `solidping_ratelimit_entries` vs the cleanup cadence, under real client diversity the bench does not produce. |
| 7 | Event-listener channels (`notifier/local.go`) | **open — not measured** | `solidping_event_listeners` + the goleak guard; needs the soak. |
| 8 | Bun prepared-statement cache | **open — not measured** | Heap base-diff for query-shape growth. |
| 9 | Prometheus label cardinality (`prommetrics/metrics.go`) | **open — not measured** | Series count is O(checks × regions × orgs); the `checks-N` scenarios vary only the first factor. |
| 10 | **`buildinfo.SQLiteDriver()` reported the wrong driver** | **fixed** — see §4 | Not on the original list, and the one that mattered most: the off-heap rule was built on it. Proven by a driver swap flipping `TestSchemaVanishesWhenDatabaseFileIsDeleted` red and back. |

The honest summary of rows 2, 3, 6, 7, 8 and 9: **the harness now exists to
settle them, and this pass did not run the soak that would.** They are recorded
as open rather than guessed at.

**Shipped guardrails** (2026-06-29, see `specs/done/2026/06/2026-06-29-04-memory-runtime-guardrails.md`):
- Runtime GC levers wired in-process with cgroup-aware `GOMEMLIMIT` auto-cap
  (Appendix B), so containers get an OOM guardrail by default.
- Postgres pool bounded (suspect #4 above).
- Argon2id derivations bounded to ≤ `min(GOMAXPROCS, 4)` concurrent, capping the
  64 MB-per-hash transient spike during login bursts
  (`internal/utils/passwords`).

---

## 9. Reduction candidates — ranked results

<<RESULTS_TABLE>>

---

## 10. Should this run in CI?

<<CI_DECISION>>

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

The soft cap and heap-growth target are applied at startup by
`internal/memlimit` (logged as `Runtime memory guardrails applied` with the
effective value and its source) and surfaced in `GET /api/mgmt/memory`
(`runtime.goMemLimitBytes`, `runtime.goMaxProcs`). **Precedence: a native
`GOMEMLIMIT` / `GOGC` env var always wins**; the `SP_RUNTIME_*` knobs and cgroup
auto-derivation only apply when the raw env var is absent.

- **`GOMEMLIMIT`** — a soft memory cap. Valuable on memory-constrained
  self-hosted boxes: the GC works harder as RSS approaches the limit, trading CPU
  to avoid the OOM killer. It is a *soft* limit — Go will still exceed it rather
  than stall forever. Three ways to set it, in precedence order:
  - native `GOMEMLIMIT=400MiB` (raw Go env, wins over everything);
  - `SP_RUNTIME_MEMORY_LIMIT=400MiB` (human size or bytes);
  - **auto** (default `SP_RUNTIME_AUTO_MEMORY_LIMIT=true`): on a container with a
    cgroup memory limit, derives the cap from that limit ×
    `SP_RUNTIME_MEMORY_LIMIT_RATIO` (default `0.9`). A no-op off-container, so
    Kubernetes pods get an OOM guardrail with zero operator action.
- **`GOGC`** — the heap-growth target (default 100 = GC when heap doubles).
  Lower trims footprint at the cost of more frequent GC; higher favors throughput
  at the cost of a larger heap. Set via native `GOGC` or `SP_RUNTIME_GC_PERCENT`
  (0 = leave the runtime default). Measured trade under login load:
  `GOGC=25` cut heap-inuse ~47% and RSS ~25% for ~2× GC cycles. Quantify with
  section 5 before turning this dial in production.
