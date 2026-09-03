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

Measured 2026-09-04. **Mode: `docker`** — `solidping-bench:local` built from the
working tree by `make bench-memory-image`, `--memory=1g --cpus=1`, on a
darwin/arm64 host (Docker Desktop's linux/arm64 VM, 10 CPUs). Protocol: warm-up
45 s, sample every 5 s for 60 s, 3 repetitions. **Not** the production
linux/amd64 image, and the warm-up is shorter than the 5 min the harness now
defaults to — both recorded here so the numbers are reproducible rather than
authoritative-looking. The `idle` row in particular is a **warm** reading, not a
settled one; the next section is why that distinction turned out to matter more
than anything else in this table.

| scenario | primary: cgroup anon+kernel | spread | rssAnon | Pss | RssFile | goTotal | heapLive | goroutines | threads |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| `idle-all-sqlite` | **148.6 MiB** | 2.8 (1.9 %) | 148.7 | 197.9 | 49.2 | 150.0 | 70.9 | 49 | 8 |
| `checks-500` | **31.3 MiB** | 0.5 (1.6 %) | 31.3 | 69.8 | 38.3 | 155.8 | 7.9 | 56 | 9 |
| `docs-crawl` | **29.5 MiB** | 0.4 (1.4 %) | 29.5 | 93.4 | 63.8 | 150.2 | 8.2 | 49 | 8 |

All values are medians (of the per-run medians); the spread is max−min across
the three repetitions.

### The headline result: 60 s of warm-up measures the startup burst, not the workload

At a 45 s warm-up, `idle-all-sqlite` sits at **148.6 MiB** while `checks-500`,
doing real work, sits at **31.3 MiB** — a 5× inversion. Re-run the same idle
scenario with a **300 s** warm-up and it settles at **23.0 MiB**:

| `idle-all-sqlite` | warm-up 45 s | warm-up 300 s |
|---|---:|---:|
| cgroup anon+kernel (primary) | 148.6 MiB | **23.0 MiB** (spread 1.1) |
| Pss | 197.9 | 44.9 |
| RssFile | 49.2 | 21.3 |
| heapLive | 70.9 | **7.4** |
| goTotal | 150.0 | 150.2 |

Boot (migrations, seeding, config) allocates a large heap. Then nothing else
allocates, so no GC runs and nothing forces the pages back — until the
scavenger gets to them, which takes minutes. `goTotal` is identical in both
columns: the runtime keeps the *address space* either way; what changes is
whether it is resident. Under load the first GC collapses it immediately, which
is why the busy scenario looked cheaper.

Three consequences an operator should act on:

- **An idle SolidPing container costs about 23 MiB of unreclaimable memory**,
  not 149. The prod API pod's 53 Mi working set in `kubectl top` is consistent
  with that: ~23 MiB anon + ~21 MiB of mapped binary + kernel accounting.
- **Any memory number taken less than ~5 minutes after boot is the startup
  burst.** `make bench-memory` therefore defaults to a **5-minute warm-up**
  (the spec proposed 60 s; 60 s was measured to be wrong) and prints a warning
  below that.
- **A "reduction" measured on a short window may be measuring nothing but
  whether a GC happened to fire.** Hence the inter-run spread, and hence
  `-floor` as a separate mode.

### Corroboration against the cluster

`kubectl top --containers` on k8xp, 2026-09-04 (linux/amd64, the real images):

| namespace | pod / container | working set |
|---|---|---:|
| `solidping-prod` | API `backend` | 61 Mi |
| `solidping-prod` | checks workers `backend` (4 regions) | 30–44 Mi |
| `solidping-prod` | checks workers `browser` sidecar | 38–119 Mi |
| `solidping-prod` | deported agent `agent` | 27 Mi |
| `solidping-dev` | API `backend` | 77 Mi |
| `solidping-dev` | checks workers `backend` (5 regions) | 21–47 Mi |

**Reading the delta.** `kubectl top` reports the cgroup **working set** =
`memory.current` − inactive file pages, so it counts anon *plus* the
actively-mapped pages of the binary. The harness's settled idle numbers —
23.0 MiB anon + 21.3 MiB RssFile ≈ 44 MiB — land just under the prod API pod's
61 Mi, and the gap is what you would expect: the pod is serving traffic (so more
of the binary is mapped in and more heap is live), it runs the amd64 image
rather than arm64, and the working set also carries kernel accounting the
`rssAnon`/`rssFile` pair does not.

The important part is that the *shape* matches: **roughly half of what
`kubectl top` shows for a SolidPing container is file-backed and reclaimable.**
Sizing a memory limit from the working set therefore over-provisions; sizing it
from `cgroup anon + kernel` (the primary metric) is what the OOM killer will
actually act on. The checks workers' 128 Mi request / 512 Mi limit against
30–44 Mi working set — of which maybe 20 MiB is anon — has room in it, but that
is a `k8xp` change and belongs in its own spec.

Note also the `browser` sidecar at 38–119 Mi: on several worker pods it is the
**larger** consumer, and it shares the pod's memory limit. Optimising the Go
process while `chromedp/headless-shell` swings by 80 Mi is optimising the wrong
container — explicitly out of scope here, but worth knowing before anyone
tightens a limit.


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

Measured with the protocol in §5 unless a row says otherwise. **A rejection is a
result**: it is recorded with its numbers so the next person does not re-try it
blind.

| # | Candidate | Δ primary metric | Verdict |
|---|---|---|---|
| 1 | GC levers as defaults (`GOGC` × `GOMEMLIMIT`) | not measured | **rejected for now, and the reason is a measurement.** The knob sweep was going to chase the 148.6 MiB idle figure — which turned out to be a warm-up artifact that the runtime resolves on its own within five minutes (23.0 MiB, §5). There is no idle overhang left for `GOGC` to attack, and `GOGC` cannot act on a process that does not allocate anyway. Appendix B's quoted "−47 % heap, −25 % RSS at `GOGC=25`" remains **unverified**; verifying it belongs with the `checks-N` scenarios, where allocation is continuous, not with idle. |
| 2 | `GOMAXPROCS` from cgroup `cpu.max` | n/a — **already done by the toolchain** | **rejected as unnecessary, verified not assumed.** In a `--cpus=1` container on a 10-CPU host: `nproc` still reports 10, `/sys/fs/cgroup/cpu.max` reads `100000 100000`, and `/api/mgmt/memory` reports `goMaxProcs: 2` — the Go ≥1.25 container-aware value (the CPU limit, with a floor of 2). The module's `go 1.26.0` directive enables it. Writing our own reader would duplicate the runtime. |
| 3 | Init-time dead weight (drop `sqliteshim`) | **0** — the premise was false | **rejected as a memory reduction, shipped as a correctness fix.** sqliteshim's driver files are mutually exclusive build-tagged files: it never linked both, so there was no dead weight to drop and no `modernc.org/libc` init to save. What the attempt *did* find is that the tags select **modernc** on every platform we ship to (only `-tags cgosqlite` selects mattn), while `buildinfo.SQLiteDriver()` reported `mattn` whenever cgo was on — see §4. |
| 4 | Per-role trimming (`role=checks` vs `role=all`) | not measured | **open.** The scenario exists (`idle-api-checks-sqlite`) but was not run in this pass. Note the structural limit recorded in §5: a checks-**only** node serves no HTTP, so it cannot be sampled from inside at all. |
| 5 | Peak-signature suspects (#1 `http.Client`, #5 10 MB body) | not measured under load | **partly closed by inspection** — see §8 rows 1 and 5. Neither is the unconditional per-check cost the runbook assumed. |
| 6 | SQLite driver and pool (mattn vs modernc) | not measured | **open, and now correctly framed.** The build runs modernc, so today's `derived.offHeapBytes` is ~0.3 MiB in an idle container — measured, and consistent with an on-heap driver. Comparing the two drivers means building with `-tags cgosqlite`; the harness can do it, this pass did not. |
| 7 | Serve embedded assets without heap copies | **−0.1 MiB (not significant, floor 1.2)** on `docs-crawl`; **no change** on `idle-all-sqlite` | **shipped on an allocation measurement, not an RSS one — stated plainly.** The RSS harness cannot resolve it: the allocation removed is short-lived, so whether it shows in resident memory depends on whether a GC fired during the window. The Go benchmark can: on `search-index.json` (3.6 MB, fetched by the offline docs search) serving goes from **3.6 MB/op to 33 KB/op** and 460 µs to 72 µs (`BenchmarkServeEmbedded{ReadFile,Streamed}`). The `idle-all-sqlite` run alongside it is the **negative control** the spec asks for: every metric came back not-significant on the scenario the change does not touch. |

### What the negative controls showed

- **Untouched scenario, untouched numbers.** `idle-all-sqlite` between the
  baseline and the candidate build: primary metric 148.6 → 148.7 MiB, and every
  one of the nine metrics flagged *not significant*.
- **The "not significant" flag fires on its own noise.** `docs-crawl`'s peak
  metric moved +18.4 MiB between the two builds and was correctly rejected
  against its own 81.1 MiB spread — a harness that reported that as a
  regression would report anything as anything.
- **One thing did trip the flag, and it is instructive**: `goTotalBytes` on
  `docs-crawl` rose 4.6 MiB (floor 4.2). Cause: with streaming, the crawl no
  longer allocates enough to force a GC, so in one repetition the process kept
  its startup heap. Less garbage produced a *higher* resident total. That is
  the same effect as the idle inversion above, and it is exactly why the
  primary metric alone is not a verdict.

### Not attempted

`GOGC`/`GOMEMLIMIT` sweeps, the 30-minute soak (suspects #2/#7/#8/#9), the
`checks-1000` peak scenarios, the mattn-vs-modernc comparison, and the
`login-burst` argon2 peak. The harness runs all of them; this pass ran the three
scenarios needed to establish a baseline and to judge the one candidate that
shipped.


---

## 10. Should this run in CI?

**No — not as a per-PR job.** Decided from the first real numbers, as the spec
required, rather than by assumption.

What the numbers say:

| metric | inter-run spread (3 reps, quiet laptop) |
|---|---|
| `cgroupUnreclaimableBytes` (primary) | 0.4–2.8 MiB → **1.4–1.9 %** |
| `heapLiveBytes` | 0.1–0.5 MiB → ~1 % |
| `goTotalBytes` | 0.1–4.2 MiB |
| `pssBytes` / `rssFileBytes` | 1.7–**32.6** MiB → up to **30 %** |
| `cgroupPeakBytes` | 20.6–**81.1** MiB → up to **33 %** |

So the primary metric is precise enough to be worth watching (a >5 % regression
would clear the noise floor), but the peak and file-backed metrics are not —
their spread is larger than most changes anyone would want to detect, on a
*quiet* machine. A shared CI runner is noisier and much slower.

The cost side is decisive on its own: a single scenario is 3 repetitions ×
(boot + 45 s warm-up + 60 s window) ≈ 6 minutes, plus building a Linux image
from the tree. Three scenarios at the default 60 s / 5 min protocol is over an
hour. That is not a per-PR job.

**Recommendation:** keep it a deliberate, local measurement. If it is ever
automated, the shape that would work is a **nightly** job on a dedicated
machine, pinned to `cgroupUnreclaimableBytes` on `idle-all-sqlite` and
`checks-500` only, alerting on a >5 % move against a stored baseline — and
explicitly *not* on the peak or Pss columns, which would cry wolf.


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
