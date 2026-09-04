---
model: opus
effort: xhigh
---

# RSS is only ever eyeballed: build a precise, repeatable RSS measurement, then test reductions against it without breaking features

## Problem

The June 2026 memory work shipped the *instruments* but never produced a
*measurement*:

- `specs/done/2026/06/2026-06-28-05-memory-consumption-analysis.md` added the
  runtime/process Prometheus collectors
  (`server/internal/prommetrics/runtime_collector.go:8-38`), the super-admin
  `GET /api/mgmt/memory` snapshot (`server/internal/app/memory_handler.go:109-176`,
  route at `server/internal/app/server.go:2080`), block/mutex pprof
  (`server/internal/profiler/profiler.go`) and the runbook
  `wiki/runbooks/memory-profiling.md`.
- `specs/done/2026/06/2026-06-29-04-memory-runtime-guardrails.md` added the
  cgroup-aware `GOMEMLIMIT` / `GOGC` levers (`server/internal/memlimit/memlimit.go`).
- The runbook's baseline table (§5, "B2") and findings table (§8, "B7",
  suspects #1–#3 and #5–#9) are **still `_tbd_`**. The only recorded numbers
  are a one-off pprof session on a macOS dev server ("~13 MB live heap, ~82 MB
  RSS, 38 goroutines"), which is not the Linux/distroless/cgroup-v2 process we
  ship.

So today "what does SolidPing cost in memory, and did change X reduce it?"
cannot be answered better than a `kubectl top` glance. Live numbers on
2026-09-04:

| Where | Container | `kubectl top` working set |
|---|---|---|
| prod API (`solidping-prod`) | `backend` | 53 Mi |
| prod checks workers (4 regions) | `backend` | 30–42 Mi |
| prod checks workers | `browser` sidecar (`chromedp/headless-shell`) | 38–120 Mi |
| dev API (`solidping-dev`) | `backend` | 60 Mi |
| dev checks workers (5 regions) | `backend` | 21–44 Mi |

Requests/limits: API 256 Mi / 1 Gi (`k8xp/k8s/solidping/base/deployment.yaml:90-96`);
checks workers 128 Mi / 512 Mi with a memory-backed `/dev/shm` that counts
against the same limit (`overlays/*-checks-*/deployment-patch.yaml`).

Why those numbers are not a measurement:

1. **Three different "RSS"es.** `kubectl top` reports the cgroup working set
   (`memory.current − inactive_file`, which *includes* actively mapped
   file-backed pages of the 242 MB binary). `process_resident_memory_bytes`
   (what `/api/mgmt/memory` calls `rssBytes`) is `/proc/self/stat` RSS =
   anon + file + shmem. Go's `Sys`/`heap_inuse` see neither the mattn cgo
   SQLite arena, OS thread stacks, nor the binary's text/rodata. Nobody has
   defined which one is the target, and the OOM killer uses a fourth
   (`memory.current` incl. unreclaimable kernel/slab).
2. **Snapshots are noise.** A local snapshot 3 s after boot reads
   `heapAlloc 72 MB, heapInuse 77 MB, Sys 91 MB, nextGc 145 MB, numGc 6,
   RSS 144 MB` — startup garbage not yet collected under `GOGC=100`, plus
   `GOMAXPROCS=10` per-P caches. Ten minutes later the same process reads
   very differently. Without a warm-up rule, a sampling window and a spread
   across repeated runs, a "10 % reduction" is indistinguishable from GC
   phase.
3. **The harness measures the wrong thing.** `make bench-checks`
   (`Makefile:195-248`, `server/cmd/loadgen`) reports throughput/latency
   from `/metrics` and never records memory; it also runs the macOS binary,
   not the container.
4. **Nothing reads the cgroup from inside.** The image is distroless
   (`Dockerfile:141`), so `memory.stat`/`smaps_rollup` cannot be read with a
   shell exec; only the process itself can report them.

Measured on the shipped image (`ghcr.io/fclairamb/solidping:0.22.0`,
`SP_RUNMODE=test`, `sqlite-memory`, `--memory=1g`; amd64 under emulation on
an arm64 host, so absolute anon numbers are inflated but the anon/file split
is kernel semantics), crawling all 1 070 embedded docs paths once:

| Moment | RssAnon | RssFile | binary mappings resident | `docker stats` |
|---|---|---|---|---|
| idle, 60 s after boot | 150 MB | 72 MB | 71 MB | 147 MiB |
| right after the crawl | 207 MB | 116 MB | 115 MB | 240 MiB |
| +60 s | 208 MB | 31 MB | 31 MB | 223 MiB |
| +180 s | 84 MB | 18 MB | 18 MB | 86 MiB |

Three lessons the harness must encode: the 46 MB of docs paged in as
file-backed memory and were reclaimed again within minutes; the `ReadFile`
heap copies (+57 MB anon) took three minutes of GC and scavenger work to
leave; and the "idle" reading at 60 s was itself 66 MB above the real
floor. A single sample cannot tell these apart, and embedded size is not an
RSS lever.

Meanwhile there are concrete, untested reduction candidates already visible
from the build:

- **Package init allocates 10.7 MB across 697 packages** (of 1 777 in the
  `serve` dependency graph, `GODEBUG=inittrace=1`). Top entries:
  `modernc.org/libc/…/netdb` 3.6 MB — linked even though the cgo build
  selects mattn, because `uptrace/bun/driver/sqliteshim` imports both
  drivers (`server/internal/buildinfo/buildinfo.go:3-30`); `k8s.io/client-go`
  scheme 0.67 MB (`internal/integrations/kubernetes`, `checkkubernetes`,
  `discovery`); `olekukonko/tablewriter` 0.66 MB (via `inbucket/html2text`);
  `jinzhu/inflection` 0.56 MB (via `bun/schema`); `encoding/gob` 0.45 MB (via
  `goja`, `gokrb5`, `go-cache`); `distribution/reference` 0.42 MB
  (`checkdocker`).
- **Embedded assets are a peak, not a baseline.** The 46 MB of docs
  (`server/internal/app/docsres`), dash0 4.5 MB, status0 1.7 MB and the
  deprecated `web/dash` build (`res`, 0.8 MB) live in rodata and are
  demand-paged, so untouched bytes cost **zero** resident memory (measured
  above). Serving them costs twice, transiently: the touched rodata pages
  become resident *file-backed* pages (clean, reclaimable, never an OOM
  cause, but visible in `kubectl top` while active), and `embed.FS.ReadFile`
  (`server.go:2619`, `:2819`, `:2892`, `:2974`) copies each file onto the
  heap per request (`[]byte(string)`), which is the only part that is
  anonymous memory.
- **747 Prometheus series at idle** (517 `solidping_*`), on a per-org
  dev instance; runbook suspect #9 says cardinality is
  O(checks × regions × orgs) and has never been measured under load.
- Runbook suspects never closed: `http.Client` per check (#1), 10 MB body
  buffer per HTTP check (#5), DEK cache growth (#2), event-listener channels
  (#7), bun statement cache (#8); plus the guardrails spec's own follow-ups
  (`JSONMap.Scan` decode path, bun query-bytes churn).

## Proposal

Two parts, in order. Part 1 is the deliverable that makes Part 2 honest;
Part 2 ships only what Part 1 proves.

### Part 1 — a precise, reproducible RSS measurement

**1a. In-process Linux memory breakdown.** Extend `/api/mgmt/memory` (keep
every existing field) and the Prometheus registry with:

- `/proc/self/status`: `RssAnon`, `RssFile`, `RssShmem`, `VmHWM`, `Threads`.
- `/proc/self/smaps_rollup`: `Pss`, `Private_Dirty`, `Private_Clean`,
  `Shared_Clean` (the binary/rodata share).
- cgroup v2 (`/sys/fs/cgroup/memory.{current,peak,max}` and `memory.stat`
  keys `anon`, `file`, `file_mapped`, `kernel`, `slab`, `sock`, `shmem`) —
  reuse the cgroup detection already in `memlimit`.
- `runtime/metrics` classes: `/memory/classes/total:bytes`,
  `heap/{objects,unused,free,released}`, `os-stacks`, `metadata/*`,
  `other`, `profiling/buckets`, and `/gc/heap/live:bytes`.
- Derived: `offHeapBytes = RssAnon − (goTotal − heapReleased)`, the number
  the runbook §4 "off-heap rule" asks for but nobody computes.

Absent files (macOS, cgroup v1, no cgroup) → fields omitted/zero, never an
error; parsers are pure functions with fixture tests. Document the new fields
in `wiki/api-specification/management.md` and `docs` where the endpoint is
described. Also expose a `?gc=1`-style *floor* mode (`runtime.GC()` +
`debug.FreeOSMemory()` before sampling) so "live floor" and "steady state"
are two named, distinct measurements.

**1b. A memory bench harness that runs the shipped shape.** Add
`server/cmd/membench` (or extend `loadgen`) plus `make bench-memory`:

- Runs the **Linux** binary inside the real image (`docker run --memory=<limit>
  --cpus=<n>`) so cgroup v2 accounting, mattn/cgo, distroless and
  `GOMEMLIMIT` auto-cap match production; a `--local` mode runs the host
  binary for quick iteration and is clearly labelled as non-authoritative.
- Fixed scenario set, each a named, parameterised workload: `idle role=all
  sqlite`, `idle role=api postgres`, `idle role=checks`, `checks N=200/500/1000`
  (reusing loadgen), `login burst` (argon2 peak), `docs crawl` (every
  `docsres` path once), `dash0 reload ×50`, `browser check ×10` (with the
  headless-shell sidecar, its container measured separately).
- Protocol: warm-up W (default 60 s), then sample `/api/mgmt/memory` every
  S (default 5 s) for D (default 5 min), K repetitions (default 3). Report
  median / p95 / max of **RssAnon, Pss, cgroup anon+kernel, cgroup peak,
  goTotal, heapLive, goroutines, Threads**, plus the spread across the K
  runs. Output a markdown table with a fixed column schema and a JSON file,
  so two runs diff cleanly.
- The **primary metric** is cgroup `anon + kernel` (what the OOM killer
  cannot reclaim); `Pss`/`RssFile` are reported separately so binary-size
  changes are visible but not conflated with heap changes. State this in the
  runbook.
- A `--compare baseline.json` mode prints deltas and flags any delta smaller
  than the baseline's own inter-run spread as "not significant".

**1c. Fill the runbook.** Replace the `_tbd_` rows in
`wiki/runbooks/memory-profiling.md` §5 and §8 with numbers from 1b, and add
the k8xp `kubectl top --containers` corroboration with the explained delta
(working set vs anon).

### Part 2 — reduction experiments, each measured, each gated

Every candidate is implemented behind a switch (build tag, config knob or
branch), measured with 1b against the same baseline, and **ships only if**
(i) the primary metric drops by more than the baseline spread on at least the
scenarios it targets, (ii) no scenario regresses, (iii) `bench-checks`
throughput and p95 latency stay within 5 %, and (iv) the full gate passes:
`make test`, `make lint`, `make test-dash0`, the **complete** dash0 Playwright
suite, and both SQLite and Postgres backends. Candidates that fail (i) are
recorded in the runbook with their numbers so nobody re-tries them blind.

Ordered by expected payoff ÷ risk:

1. **GC levers as defaults.** Measure `GOGC ∈ {100, 50, 25}` × `GOMEMLIMIT`
   auto-cap on the `checks N=500` and `idle` scenarios, with
   `gcCpuFraction` and bench-checks throughput as the cost side. Decide
   whether `SP_RUNTIME_GC_PERCENT` / ratio defaults change (runbook Appendix
   B quotes "−47 % heap, −25 % RSS for ~2× GC cycles" — verify it).
2. **`GOMAXPROCS` from cgroup `cpu.max`.** k8xp sets CPU *limits* (1000m) but
   the runtime sees node CPUs; measure per-P cache cost, then cap (own
   reader, `automaxprocs`, or `SP_RUNTIME_MAX_PROCS`), respecting a native
   `GOMAXPROCS` env like `memlimit` does.
3. **Init-time dead weight.** Replace `sqliteshim` with a build-tagged direct
   import so only the selected driver links (drops `modernc.org/libc` from
   cgo builds and mattn from non-cgo builds; keep `buildinfo.SQLiteDriver()`
   truthful). Then measure each remaining top-10 init contributor and move
   what can move behind lazy init or a lighter import (e.g. `html2text`
   without `tablewriter`). Report `inittrace` totals before/after.
4. **Per-role trimming.** Measure `idle role=checks` vs `idle role=all`; make
   sure a checks worker does not build API-only state (rate limiter, DEK
   cache, event notifier fan-out, the full Prometheus handler set, embedded
   frontends' routes). Anything found gets a startup guard on
   `config.NodeRole` (`server/internal/config/config.go:28-35`).
5. **Runbook suspects with a peak signature** — #1 `http.Client` per check
   (transport reuse), #5 10 MB body buffer (bounded/streaming reader) — on
   the `checks N=1000` scenario; #2/#7/#8/#9 on a 30-min soak of the same
   scenario, using the 1b spread to call "flat" vs "growing".
6. **SQLite driver and pool.** mattn (cgo, off-heap) vs modernc (on-heap) on
   `idle` and `checks N=500`; the `cache=shared` connection string and
   `SetMaxOpenConns(1)` (`server/internal/db/sqlite/sqlite.go:146-156`) make
   this the cheapest place to measure the cgo arena.
7. **Serve embedded assets without heap copies (peak-only).** Idle RSS does
   not depend on embedded size (crawl table above), so this only targets
   the `docs crawl` / `dash0 reload` peak and the working-set visibility of
   touched rodata. Replace the `embed.FS.ReadFile` + write pattern with
   `http.ServeContent`/`io.Copy` over the `fs.File` (no anonymous copy; the
   pages stay file-backed and reclaimable), keep the cache/content-type
   policy of `writeDocsFile`, and optionally pre-compress at build
   (`.br`/`.gz`, served by `Accept-Encoding`) so fewer pages are touched per
   request. Expect no change on the idle scenarios and report that as a
   negative control.

Explicitly **not** changed by this spec without a separate decision: the
argon2 memory parameter (64 MiB per hash is a security setting — measure the
`login burst` peak and report it, do not lower it), the dashboard/status/docs
feature set, and worker concurrency or lease semantics.

### Out of scope

- The browser sidecar's own memory (`chromedp/headless-shell`) — measured as
  its own container so it does not pollute the backend numbers, but not
  optimised here.
- Frontend (browser-side) memory of dash0/status0.
- A continuous-profiling stack (Pyroscope / parca); the runbook may note it
  as a follow-up.
- Changing k8xp requests/limits — the numbers from 1c may motivate it, but
  that is a `k8xp` repo change.

### Open questions

- Is the deprecated `web/dash` build (`internal/app/res`, `//go:embed all:res`
  at `server.go:172`) still required at runtime? Dropping it shrinks the
  binary and the image, not RSS (untouched rodata is never resident); it is
  a product decision, worth doing for hygiene only.
- The Docusaurus API reference HTML (`docsres/api`, 24 MB, 546 files)
  duplicates the interactive `/openapi` explorer. Same caveat: dropping it
  is an image-size and crawl-peak win, not an idle-RSS win. Keep both, or
  link the docs to `/openapi` and drop the static build?
- Should 1b run in CI (`.github/workflows/ci.yml`) as a non-blocking job
  that posts the table, so regressions are seen per PR? Runner noise may
  make the K-run spread too wide to be useful; decide from the first
  real numbers.

### Testing / verification

- 1a: fixture tests for every parser (`/proc/self/status`, `smaps_rollup`,
  cgroup v2 `memory.stat`, missing files, cgroup v1 layout); handler test
  asserting the `{data}` envelope, camelCase, and that macOS/no-cgroup
  yields omitted fields rather than an error; Prometheus registration test
  (no duplicate descriptors with the existing collectors).
- 1b: a unit test on the aggregation (median/p95/spread, "not significant"
  rule) with synthetic samples; one end-to-end `make bench-memory --local`
  run in `make test`-adjacent CI to prove the tool starts, samples and
  writes both output files.
- Part 2: each shipped candidate carries (a) the before/after table from 1b
  in its commit message or the runbook, (b) a regression test where the
  change has a behavioural surface (e.g. content-type/cache headers and
  `Accept-Encoding` negotiation for candidate 4; role gating for 5;
  `buildinfo.SQLiteDriver()` for 3; native-env precedence for 1/2).
- Negative controls, per the audit-gate rule: a candidate that claims a
  reduction must also show the harness detecting *no* change on an
  untouched scenario, and the "not significant" flag firing on a no-op
  rerun of the baseline.

### Deliverables

1. Code: 1a breakdown (endpoint + metrics + parsers + tests); 1b
   `membench` + `make bench-memory`; every Part 2 candidate that passed the
   gate, each as its own commit with its numbers.
2. Docs: `wiki/runbooks/memory-profiling.md` §4/§5/§8 filled in with real
   numbers and the metric definitions; `wiki/api-specification/management.md`
   and the docs-site page for the new fields; `Makefile`/`server/CLAUDE.md`
   entries for the new target.
3. A ranked results table (candidate → Δ primary metric ± spread → shipped /
   rejected / needs-decision) at the end of the runbook, so the next memory
   spec starts from data.

## Resolved open questions

Answered by the user on 2026-09-04, before implementation started. These are
directives, not suggestions — implement to them.

**Q. Is the deprecated `web/dash` build (`internal/app/res`, `//go:embed all:res`
at `server.go:172`) still required at runtime? Dropping it shrinks the binary
and the image, not RSS (untouched rodata is never resident); it is a product
decision, worth doing for hygiene only.**

**Decision: defer it to its own spec — do NOT touch `web/dash` here.** Leave the
`res` embed and any routes serving it exactly as they are. The rationale is
scope, not merit: this is a binary/image-size question, and the spec itself
notes it yields no RSS win, so it does not belong in a memory-measurement spec
and must not compete with the seven measured Part 2 candidates. If the binary
size is worth attacking, that is a separate spec.

**Q. The Docusaurus API reference HTML (`docsres/api`, 24 MB, 546 files)
duplicates the interactive `/openapi` explorer. Same caveat: dropping it is an
image-size and crawl-peak win, not an idle-RSS win. Keep both, or link the docs
to `/openapi` and drop the static build?**

**Decision: keep both — do NOT drop the static API reference.** The 24 MB is
rodata (image size only, zero resident cost). The static build is crawlable and
works without JavaScript; the `/openapi` explorer is interactive but neither.
Dropping it would lose search-indexed API documentation for a saving this spec
explicitly does not care about. Revisit only as a deliberate image-slimming
spec if deploy size becomes the problem.

**Q. Should 1b run in CI (`.github/workflows/ci.yml`) as a non-blocking job that
posts the table, so regressions are seen per PR? Runner noise may make the K-run
spread too wide to be useful; decide from the first real numbers.**

**Decision: as the spec already directs — decide from the first real numbers.**
Run 1b locally first, read the inter-run spread, and only wire the CI job if the
spread is tight enough for a regression to be distinguishable from runner noise.
If it is not, record the measured spread and the "not wired, and why" conclusion
in `wiki/runbooks/memory-profiling.md` rather than shipping a job that will cry
wolf. Either way the outcome is a documented number, not an unexplained absence.
