# Fast/slow check lanes — a sleep checker, a browser→sleep load harness, and a data-gated decision on splitting `check_jobs`

## Context

The question behind this spec: **should the scheduler run two lanes — fast and
slow — instead of one queue?** Rather than answer it by intuition, this spec
builds the tooling to *measure* it first, then commits to a design gated on that
measurement.

Why now: spec `2026-06-30-09` (cost-aware, plan-weighted scheduling) shipped two
mechanisms for taming slow checks — (a) **soft** WFQ ordering
(`effective_scheduled_at = scheduled_at + cost_ewma + delay_ewma − tier_credit`,
the claim `ORDER BY` key) and (b) a **hard** per-worker in-memory admission cap
(`availableSlowRunners`, `admission.go`) that guaranteed fast checks a share of
runner slots. **This batch is removing (b)** — `admission.go` /
`admission_test.go` are deleted in the working tree and the logic folded down —
leaving only the soft ordering. Soft ordering deprioritizes a slow check but does
**not** guarantee a fast check a slot: under a slow backlog, a single
`ORDER BY effective_scheduled_at LIMIT n` claim can be entirely consumed by the
most-overdue jobs, which may all be slow. So the head-of-line failure mode spec
009 named is partially back, and the natural next move is to make lane isolation a
property of **storage + the claim query** (fleet-correct) rather than per-worker
memory (the documented weakness in 009's D5).

The user's instinct: add a fast/slow column to `check_jobs`, index each lane
separately, and claim *"up to N fast and M slow per batch."* They flagged the
fetch API as the uncertain part (*"it might not be the best idea"*). This spec
takes that seriously — it critiques the static split and recommends a
reservation-style claim instead — but only **after** we have data.

The four user-requested steps map 1:1 to the four phases below:

1. A **`sleep` checker** — sleeps for a configured number of milliseconds. Its
   cost is exactly `sleep_ms`, so it is a precise, deterministic load generator
   for exercising the whole scheduler (cost EWMA, cost-aware timeout, delay EWMA,
   lane classification) without any real network.
2. **Replace the current instance's `browser` checks with sleep checks** —
   turning heavy, nondeterministic headless-Chrome load into a dial we can set to
   any fast/slow mix.
3. **Analyze the `cost_ewma_ms` / `delay_ewma_ms` values** the harness produces —
   the go/no-go evidence.
4. **Decide** whether and how to physically split `check_jobs` into fast/slow
   lanes — the design, gated on step 3.

> **Extends, not duplicates,** `2026-06-30-09-check-execution-fairness-…`. That
> spec introduced the cost/delay EWMA and WFQ ordering; this one asks whether to
> promote the (now-removed) in-memory lane cap into a storage-level partition.
> Distinct from `2026-06-30-07-adaptive-recovery-flap-backoff-redesign` (incident
> state machine, a different layer).

---

## Current state (verified against source — line numbers as-of-writing; the
checkworker package is actively changing on this batch branch, re-verify at build)

| Concern | Location | Today |
|---|---|---|
| Claim gate **and** order | `checkworker/checkjobsvc/service.go:288,294` | `WHERE effective_scheduled_at <= now(+maxAhead)` … `ORDER BY effective_scheduled_at ASC LIMIT n`; PG `FOR UPDATE SKIP LOCKED` (`:307`), SQLite optimistic |
| De-prioritization mechanism | scheduling math (`scheduling/scheduling.go`) | **Soft only.** `effective_scheduled_at` pushes slow/late jobs later. **No hard slot guarantee** for fast checks any more |
| In-memory slow-lane admission cap | `checkworker/admission.go` | **Being deleted this batch** (working-tree `D`). `availableSlowRunners` gone; only comments remain (`worker.go:101`) |
| Cost / delay EWMA write | post-exec write, `service.go` `ReleaseLeaseWithSchedulingState` (~`:398–444`); `worker.go` `releaseLeaseWithCost` (~`:715`) | `cost_ewma = α·dur + (1−α)·cost_ewma` (timeout → 30 s ceiling); `delay_ewma = α·(start−effective, ⌊0⌋) + …`; **one UPDATE, no new hot-path query** |
| Cost-aware timeout | `worker.go` `executeJob` (~`:600`), `scheduling.Params.ExecutionTimeout(cost)` | `clamp(k·cost_ewma, floor, 30s)`; timeouts pin cost to ceiling |
| Runner pool | `worker.go:98–104` | single global `jobsChan`; `availableRunners atomic.Int32`; `Nb` default **25** (`config.go`, raised 3→25 this batch) |
| Fetcher | `worker.go` `fetchAndDistributeJobs` (~`:315`) | claims ≤ `availableRunners` jobs, one class, one query |
| `check_jobs` cost columns | `db/models/check_job.go:38–53` | `cost_ewma_ms`, `delay_ewma_ms`, `plan_weight`, `effective_scheduled_at` |
| Indexes on `check_jobs` | migrations 001 + 006 | `…scheduled_at_idx`, `idx_check_jobs_effective_scheduled_at`, `check_uid` uniques. **No lane / class column or index** |
| Migrations | `db/{postgres,sqlite}/migrations/` | last is **`007_check_delay_ewma`**; next is **`008_…`**, both dialects |
| `browser` checker | `checkerdef/types.go:165,270,361`; `checkers/checkbrowser/` | headless Chrome; `labelUnsafe, labelReqChrome, labelCatOther`. Heavy, high-variance cost |
| Synthetic / `sleep` checker | — | **None.** No way to generate controllable check load; load-testing depends on real endpoints |
| Cost/delay visibility | `prommetrics/metrics.go` | execution-duration histogram + `check_scheduling_delay_seconds`; **per-job `cost_ewma_ms`/`delay_ewma_ms` are not surfaced** for analysis |

**Diagnosis.** We have a continuous soft-priority key and, as of this batch, no
hard lane isolation. Whether that is a problem is an empirical question about the
*shape* of the cost distribution: if check cost is **bimodal** (a cluster of
~sub-second checks + a cluster of multi-second/timeout checks) the two mix badly
in one queue and hard lanes pay off; if it is a **smooth continuum**, the
existing `effective_scheduled_at` ordering already handles it proportionally and a
2-lane quantization buys little complexity-for-benefit. We cannot see the
distribution today, and real endpoints are a noisy way to probe it. Hence: build
a deterministic load source, measure, then decide.

---

## Design decisions

### D1 — Answer "do we need lanes?" with data, not intuition (Phase 4 is gated)

The sleep checker + browser conversion (Phases 1–2) exist to make Phase 3's
measurement possible. **Phase 4 ships only if Phase 3 shows the head-of-line
failure mode is real** under a representative fast+slow mix — specifically, that
fast checks' `delay_ewma_ms` inflates when slow checks saturate the pool (i.e.
soft ordering alone does not keep fast checks fresh). If WFQ ordering keeps fast
checks on time, the split is unnecessary complexity and we stop after Phase 3 with
a written finding. This is the honest reading of *"I'd like to understand if we
shouldn't…"* — it is a question, and the spec keeps it one until measured.

### D2 — The `sleep` checker's cost **is** its config → it is the ideal probe

`cost_ewma_ms` converges to a check's real execution duration. A checker that
simply sleeps `sleep_ms` therefore lets us place a job at **any exact point** on
the fast↔slow axis and directly assert the EWMA/classification/timeout machinery.
Sleeping via `select { case <-time.After(d): case <-ctx.Done(): }` also means a
`sleep_ms` above the job's cost-aware timeout is **interrupted and pinned to the
ceiling** — reproducing a timing-out endpoint deterministically. An optional
forced `status` and `jitter_ms` synthesize failure load and EWMA smoothing. No
other checker gives this control. It is a general test asset well beyond this
spec (scheduler benchmarks, fairness tests, dashboards demos).

### D3 — Lanes are **hard isolation layered on** soft WFQ, not a replacement

If we build Phase 4, the lane split does **not** remove `effective_scheduled_at`
ordering — the two compose exactly as 009's D1 argued: **lanes** give a hard
capacity floor (a fast burst always finds a claimable fast row), **ordering**
gives fairness + anti-starvation *within* each lane. The lane split is really
"009's admission cap, re-homed from per-worker memory into the table + claim
query," which makes it correct across a distributed fleet (009 D5) and lets us
delete in-memory coordination for good.

### D4 — Critique of *"max N of each"*: prefer a **reservation**, not a static split

The user's doubt is well-founded. A fixed *"claim up to N fast + M slow"* is
simple but **not work-conserving**: when the slow lane is empty, its M slots sit
idle while fast work waits, and vice-versa. The better shape is a **reserved
fast floor with borrowing**:

> Reserve `F` slots that **only fast** jobs may occupy. Claim fast up to the free
> pool first; let slow fill whatever remains **above** the reserve. A slow
> backlog can borrow all non-reserved capacity (work-conserving) but can **never**
> starve a fast burst (the floor is inviolable). This is the admission cap
> expressed in the claim itself.

Static N/M is acceptable as a v1 if it is simpler to land, but the recommendation
is the reservation form. Either way the claim reads the two **partial indexes**
(below), so the fast path never scans slow rows.

### D5 — Two lanes, not N; derive with **hysteresis**

Cost is a continuum, but the failure mode is binary (a slot is either held by a
head-of-line hog or not). Two lanes capture that; a third buys little against real
complexity. The `lane` is **derived** from `cost_ewma_ms` and maintained in the
**existing** post-exec write (no new hot-path query), with a **dead-band** to stop
a check hovering at the threshold from flipping lanes every run and churning the
index: promote to slow at `cost > slow_threshold_ms`, demote to fast only at
`cost < fast_threshold_ms` (`fast_threshold < slow_threshold`).

---

## Recommended implementation (phased; 1–2 are prerequisites, 3 is the gate, 4 is
conditional)

### Phase 1 — the `sleep` checker (small, safe, independently valuable)

A greenfield checker modelled on the smallest existing ones (`checkheartbeat`
config.go is 30 lines; `checktcp` is the active-Execute template) — no network, no
secrets. Follow the six steps in `server/internal/checkers/CLAUDE.md` and the four
wiring points (as `2026-06-29-02-ntp-protocol-check` did):

1. `CheckTypeSleep CheckType = "sleep"` in `checkerdef/types.go` const block.
2. A `CheckTypeMeta` in `checkTypesRegistry` — **required** or `activation_test.go`
   fails: `{Type: CheckTypeSleep, Labels: []string{labelSafe, labelStandalone,
   labelCatOther}, Description: "Sleep for a fixed duration (synthetic/testing)",
   DefaultPeriod: 1 * time.Minute}`.
3. Add `CheckTypeSleep` to `ListCheckTypes()`.
4. Import + a `case` in **both** `registry.go` switches (`GetChecker`, `ParseConfig`).

New `server/internal/checkers/checksleep/{config.go,checker.go,samples.go,checker_test.go}`:

```go
type SleepConfig struct {
    SleepMs  int    `json:"sleep_ms,omitempty"`  // required, >0, ≤ cap (e.g. 120000)
    JitterMs int    `json:"jitter_ms,omitempty"` // optional, ≥0 and < SleepMs; ± random
    Status   string `json:"status,omitempty"`    // optional: ""|up|down|timeout|error (default up)
}
```

- `Validate` (no network): `sleep_ms > 0` and `≤ cap`; `jitter_ms ≥ 0 && < sleep_ms`;
  `status ∈ {"",up,down,timeout,error}`; auto-name/slug (e.g. `sleep-500ms`).
- `Execute`: `d := sleep_ms ± rand(jitter_ms)`; then
  `select { case <-time.After(d): … case <-ctx.Done(): return StatusTimeout }`.
  Honor `ctx` so the cost-aware timeout interrupts (D2). If `Status` forced,
  return it after sleeping. `Result.Duration = actual slept`; `Metrics{"sleep_ms": …}`.
- `samples.go`: a fast sample (`sleep_ms: 200`) and a slow one (`sleep_ms: 8000`).
- **Open decision — UI exposure.** It is a synthetic checker, not a customer
  feature. Options: (a) register backend-only and create it via API/CLI only;
  (b) wire it into `check-form.tsx` behind a dev/test flag; (c) full UI with a
  clear "synthetic/testing" tag. Recommend **(a)** for v1 (Phase 2 creates them
  programmatically) — cheapest, keeps the product surface clean. Revisit if it
  proves useful as a demo. (Note: `activation_test.go` still requires the meta to
  be registered regardless of UI exposure.)

### Phase 2 — browser→sleep load harness (current instance only, reversible)

A dev/admin one-shot — a `solidping` CLI subcommand (e.g. `solidping dev
browser-to-sleep`) or a scripted API loop — that, for **every** check of type
`browser` in the current instance, converts it to a `sleep` check. Seed each
`sleep_ms` from that check's **current `cost_ewma_ms`** (fallback to a sane
default when 0) so the synthetic load **mirrors the cost distribution** of the
real browser load it replaces — i.e. the scheduler sees the same pressure without
Chrome's CPU/RAM/nondeterminism. This is an **operational/data step, not a schema
migration**; it is scoped to the running instance and reversible via DB reset in
dev. Guard it so it cannot run against production data by accident (dev/test
runmode check, explicit `--yes`).

Deliverable: after running it, the instance's load is entirely synthetic and
tunable — set some sleep checks fast (`200ms`), some slow (`8s`), some to
`timeout`, to synthesize the exact mix Phase 3 needs.

### Phase 3 — surface & analyze the EWMA values (**the go/no-go gate**)

Make `cost_ewma_ms` / `delay_ewma_ms` observable, then characterize them under the
Phase-2 harness:

1. **Read-only visibility.** Add a debug/admin surface — a `GET
   /api/mgmt/scheduling/cost-distribution` (or extend an existing mgmt endpoint)
   returning percentiles (`p50/p90/p99/max`) of `cost_ewma_ms` and
   `delay_ewma_ms` across `check_jobs`, plus fast/slow counts at a candidate
   threshold. SQL: `percentile_cont(...) within group (order by cost_ewma_ms)`.
   Keep cardinality sane — **no per-job Prometheus labels**; this is an aggregate
   query, not a time series.
2. **Characterize.** With a controlled fast+slow+timeout mix, answer:
   - **Bimodal or continuous?** Is there a clean fast cluster + slow cluster
     (→ lanes help, D5) or a smooth ramp (→ WFQ ordering already suffices)?
   - **What fraction is "slow"** at plausible thresholds (1s / 2s / 5s)?
   - **Head-of-line, quantified:** drive the slow lane to saturate the pool and
     watch **fast checks' `delay_ewma_ms`**. If it inflates (fast checks start
     late because slow checks hold the slots), the admission-cap removal has a
     real cost and **Phase 4 is justified**. If fast `delay_ewma_ms` stays near
     zero, soft ordering is enough — **stop here, write the finding.**
3. **Output:** a short written analysis appended to this spec (or a follow-up
   note) recording the distribution and the go/no-go call.

### Phase 4 — the fast/slow lane split (**only if Phase 3 says so**)

- **`lane` column** on `check_jobs` (`smallint`/enum: 0=fast, 1=slow), derived
  from `cost_ewma_ms` with **hysteresis** (D5), written in the existing post-exec
  UPDATE — **no new hot-path query**. New rows start `fast`.
- **Partial indexes per lane** (migration `008`, both dialects):
  `create index … on check_jobs (effective_scheduled_at) where lane = 0;` and the
  same `where lane = 1;` — the fast claim scans only fast rows; smaller, hotter
  index. (Composite `(lane, effective_scheduled_at)` is the single-index
  alternative; benchmark both with `make bench-checks`.)
- **Claim = reservation, not static split (D4).** Two-part claim per fetch:
  fast first up to the free pool; slow fills the remainder **above** a reserved
  fast floor `F`. Preserve the current gate/order key (`effective_scheduled_at`)
  **within** each lane (D3). This replaces — and lets us finish deleting — the
  in-memory admission machinery, fleet-correctly (009 D5).
- **Config (tunable, no redeploy;** mind the multi-word `SP_*` env quirk
  [[project_koanf_env_quirk]]**):** `scheduling.lane_slow_threshold_ms`,
  `scheduling.lane_fast_threshold_ms` (hysteresis band),
  `scheduling.fast_lane_reserved` (`F`), optional `scheduling.slow_lane_max`.
  Defaults chosen so behavior ≈ today (single effective pool) until tuned.
- **Distributed honesty:** the reservation is enforced in the claim query against
  shared table state, so it *is* a global guarantee (unlike 009's per-worker cap)
  — modulo `SKIP LOCKED` racing, which only ever *under*-claims, never violates
  the floor.

---

## Out of scope

- **Preemption of running checks** — isolation is by admission/claim only, never
  cancelling an in-flight probe (unchanged from 009).
- **More than two lanes** / a true global WFQ virtual clock (D5, 009 D3).
- **Exposing `sleep` as a customer-facing product feature** — it is synthetic;
  UI exposure is at most a dev/test convenience (Phase 1 open decision).
- **Auto-tuning thresholds** — thresholds are static config; adaptive tuning is a
  later lever.
- **Building Phase 4 speculatively** — it is explicitly gated on Phase 3 (D1).

---

## Verification

```bash
make dev-test       # backend + dash0 on :4000
make test           # backend unit/integration
make migrate        # apply 008 (Phase 4) on fresh AND populated DB, both dialects
make bench-checks   # claim-path throughput: partial-index vs composite, no regress
make lint           # no new findings — never relax config [[feedback_lint_strict]]
```

- **Sleep checker (unit, table-driven, `testify/require`, `t.Parallel()` — see
  `server/CLAUDE.md`):** `Validate` (missing/zero `sleep_ms`→error; cap;
  `jitter ≥ sleep`→error; bad `status`→error; name/slug autofill). `Execute` with
  a tiny `ctx` timeout < `sleep_ms` → `StatusTimeout` (proves ctx honored, D2);
  `status:"down"`→down; `jitter_ms:0` for deterministic timing;
  `Duration≈sleep_ms`; `sleep_ms` metric present. **No sleeping for seconds in
  tests** — use ms-scale sleeps and injected `ctx`.
- **EWMA convergence (unit/integration):** a `sleep_ms:500` job's `cost_ewma_ms`
  converges toward 500 over N runs; a `sleep_ms` above the cost-aware timeout
  pins `cost_ewma_ms` to the ceiling.
- **Browser→sleep harness:** on a seeded dev DB, all `browser` checks become
  `sleep` checks with `sleep_ms` seeded from prior `cost_ewma_ms`; guarded against
  non-dev runmode; idempotent.
- **Cost-distribution endpoint:** returns correct percentiles/counts on a known
  fixture; read-only; org-scoped/admin-guarded; low cardinality.
- **Phase 4 (if built):** migration `008` on fresh + populated DBs, both dialects,
  incl. stale dev DBs [[project_migration_consolidation_stale_db]] — existing rows
  get `lane` from their `cost_ewma_ms`. Scheduler test: slow-lane backlog saturates
  the pool → assert fast jobs remain claimable and the reserved floor `F` is never
  breached; hysteresis: a check oscillating around the threshold does **not** flip
  lanes each run. `make bench-checks` before/after the new indexes.
- **Manual (the whole point):** with the harness, set 90% fast / 10% slow-timeout,
  saturate, and compare fast checks' `delay_ewma_ms` **with vs without** Phase 4.
  Prefer Playwright for any UI [[feedback_browser_testing]]; treat intermittent
  failures as bugs [[feedback_flaky_tests_are_bugs]].

---

## Key files

| File | Change |
|---|---|
| `server/internal/checkers/checksleep/{config,checker,samples,checker_test}.go` | **+** new synthetic checker |
| `server/internal/checkers/checkerdef/types.go` | **~** `CheckTypeSleep` const, `CheckTypeMeta`, `ListCheckTypes` |
| `server/internal/checkers/registry/registry.go` | **~** import + `case` in `GetChecker` and `ParseConfig` |
| `server/cmd/...` or a script | **+** Phase-2 `browser→sleep` dev/admin conversion (guarded, reversible) |
| `server/internal/handlers/.../mgmt` (or existing mgmt) | **+** Phase-3 cost/delay distribution read-only endpoint |
| `server/internal/db/{postgres,sqlite}/migrations/008_check_job_lane.{up,down}.sql` | **+** *(Phase 4)* `lane` column + partial indexes, both dialects |
| `server/internal/db/models/check_job.go` | **~** *(Phase 4)* `Lane` field + zero-default |
| `server/internal/checkworker/checkjobsvc/service.go` | **~** *(Phase 4)* per-lane reservation claim; fold `lane` into the post-exec write |
| `server/internal/checkworker/worker.go` | **~** *(Phase 4)* fetcher claims fast/slow per the reserve; finish removing in-memory admission remnants |
| `server/internal/checkworker/scheduling/scheduling.go` | **~** *(Phase 4)* `lane` classifier + hysteresis (pure math, unit-tested) |
| `server/internal/config/config.go` | **~** *(Phase 4)* `scheduling.lane_*` / `fast_lane_reserved` keys + env reader |
| `server/internal/checkers/CLAUDE.md` / docs | **~** note the synthetic `sleep` checker; do **not** count it in the customer "N check types" tally |

---

## Risk log

| Risk | Mitigation |
|---|---|
| Building lanes we don't need (complexity for nothing) | **Gate on Phase 3 data** (D1); if fast `delay_ewma_ms` stays low under a slow backlog, stop after Phase 3. |
| Static "N of each" wastes capacity | Recommend **reservation + borrowing** (D4); static split only as a documented v1. |
| `lane` flapping churns the index | **Hysteresis** dead-band (D5); classify in the existing post-exec write, no extra query. |
| `sleep` checker leaks into the product / customers create it by accident | Backend-only v1 (Phase 1 open decision); clear synthetic label; excluded from the customer check-type tally. |
| Phase-2 conversion run against real/prod data | Dev/test runmode guard + explicit `--yes`; reversible in dev; never a schema migration. |
| Test suite made slow by real sleeps | ms-scale sleeps + injected `ctx`; `jitter_ms:0` for determinism; never seconds-long waits [[feedback_flaky_tests_are_bugs]]. |
| New partial indexes regress the claim hot path | Benchmark partial vs composite with `make bench-checks` before/after; keep the effective-time gate/order within lane. |
| Migration 008 skipped on stale dev DBs | Both dialects; verify on fresh + populated + pre-consolidation DBs [[project_migration_consolidation_stale_db]]. |
| Removing the in-memory cap (this batch) before lanes land reintroduces head-of-line blocking in the interim | Acknowledged: soft WFQ ordering still deprioritizes slow work; Phase 3 measures the residual harm and sizes the urgency of Phase 4. |

**Status**: Todo | **Created**: 2026-07-01 | **Extends**: `2026-06-30-09-check-execution-fairness-cost-deprioritization-plan-weighting.md`

---

## Implementation Plan

1. **Phase 1 — `sleep` checker.** `checksleep` package (config/checker/samples/tests);
   four registry wiring points; `activation_test.go` green. Backend-only exposure.
   `make build && make lint && make test`.
2. **Phase 2 — browser→sleep harness.** Guarded, reversible dev/admin conversion;
   seed `sleep_ms` from each browser check's `cost_ewma_ms`. Run it on the current
   instance so its load becomes synthetic and tunable.
3. **Phase 3 — measure (the gate).** Read-only cost/delay distribution endpoint;
   drive fast/slow/timeout mixes; record the distribution and, critically, whether
   fast checks' `delay_ewma_ms` inflates under a slow backlog. **Write the go/no-go
   finding into this spec.**
4. **Phase 4 — lanes (only if Phase 3 says yes).** Migration `008` (`lane` + partial
   indexes, both dialects); hysteresis classifier folded into the post-exec write;
   reservation-style two-part claim; delete the in-memory admission remnants;
   `scheduling.lane_*` config. Scheduler + migration + bench tests.

> **Open decisions to confirm before building:** (a) whether Phase 4 proceeds at
> all — decided by Phase 3; (b) reservation-borrowing vs static N/M for the claim
> (D4 recommends reservation); (c) `sleep` UI exposure (recommend backend-only);
> (d) the slow/fast hysteresis thresholds and reserved-fast floor `F`.

---

## Phase 3 finding — go/no-go

**Status: the measurement TOOLING is complete and shipped; the empirical
measurement is DEFERRED to real operation; Phase 4 remains GATED and is NOT
built.**

### (a) The tooling is complete and ready

All three measurement instruments landed on this batch and are covered by tests:

- **Phase 1 — `sleep` checker** (`server/internal/checkers/checksleep/`): a
  synthetic checker whose cost equals its configured `sleep_ms`, honoring `ctx`
  so the cost-aware timeout interrupts it (a `sleep_ms` above the timeout pins
  `cost_ewma_ms` to the ceiling, reproducing a timing-out endpoint). Optional
  `jitter_ms` and forced `status` synthesize EWMA smoothing and failure load.
  Registered backend-only (all four wiring points), excluded from the customer
  check-type tally.
- **Phase 2 — browser→sleep harness** (`solidping dev browser-to-sleep`,
  `server/internal/devtools/browsertosleep/`): converts every `browser` check
  (and its `check_jobs`) into a `sleep` check, seeding each `sleep_ms` from the
  check's current `cost_ewma_ms` (fallback 1000 ms), so the instance's load
  becomes fully synthetic and tunable while mirroring the real cost
  distribution. Guarded (refuses a SaaS deployment; requires `--yes`) and
  idempotent.
- **Phase 3 — cost-distribution endpoint**
  (`GET /api/mgmt/scheduling/cost-distribution`,
  `server/internal/scheduling/costdist/`): a read-only, super-admin-guarded,
  low-cardinality aggregate returning p50/p90/p99/max of `cost_ewma_ms` **and**
  `delay_ewma_ms` across `check_jobs`, plus fast/slow counts at a candidate
  `thresholdMs` (default 1000). Dialect-aware: PostgreSQL `percentile_cont(...)
  within group`, SQLite computes the same continuous percentiles in Go
  (validated against embedded Postgres on the same fixture). Documented in
  `openapi.yaml`. This is an on-demand aggregate, not per-job Prometheus labels.

Together these let an operator dial in any fast/slow/timeout mix, saturate the
pool, and read the resulting cost/delay distribution directly.

### (b) The empirical measurement is DEFERRED (operational, not automatable)

The actual go/no-go evidence — driving a representative fast+slow+timeout mix
against a **running, loaded** instance, saturating the runner pool, and
observing whether **fast checks' `delay_ewma_ms` inflates** when slow checks
hold the slots — is an operational exercise. It requires a live server under
sustained load and cannot be produced in this automated implementation
environment (it is not a unit/integration assertion; it is a load experiment
observed over time). It is therefore deferred to real operation. The procedure
is exactly the "Manual" step in the Verification section: run
`solidping dev browser-to-sleep`, tune a ~90% fast / ~10% slow-timeout mix, load
the instance, and poll `GET /api/mgmt/scheduling/cost-distribution` — watching
whether the fast cohort's `delay_ewma_ms` stays near zero (soft WFQ ordering
suffices → stop) or inflates (head-of-line blocking is real → Phase 4 justified).

### (c) Phase 4 stays GATED and is NOT built (per D1)

Consistent with design decision **D1**, the fast/slow lane split (migration 008,
the `lane` column, partial indexes, and the reservation-style claim) is **not**
built speculatively. It ships only once the deferred measurement shows the
head-of-line failure mode is real under a representative mix. Until then the
scheduler keeps only the soft `effective_scheduled_at` (WFQ) ordering; the
tooling above exists precisely so that decision is made on data, not intuition.
