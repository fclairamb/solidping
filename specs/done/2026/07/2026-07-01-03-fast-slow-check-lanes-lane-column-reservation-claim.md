# Fast/slow check lanes — `lane` smallint on `check_jobs`, partial indexes, reservation-style claim

## Context — the Phase-3 gate is answered: GO

Spec `2026-07-01-01` built the measurement tooling (sleep checker,
browser→sleep harness, cost-distribution endpoint) and **gated the lane split
(its Phase 4) on data**: build lanes only if the cost distribution is bimodal
and fast checks' start delay inflates under a slow backlog. That measurement
was deferred to real operation. It has now been taken (2026-07-01, dev
instance, `server/solidping.db`, 74 jobs, pool 25) — **this spec records the
finding, and the finding is GO**:

| Signal | Measured |
|---|---|
| Cost distribution | **Bimodal.** Slow cluster: 20× `browser` ≈ 10 000 ms, 1× `mongodb` pinned at 30 000 ms (timeout ceiling), 4× `sleep` 500–5 000 ms. Fast cluster: ~49 checks < 1 000 ms, most < 100 ms. No middle. |
| Offered slow load | The 20 browser checks run at 10 s period × ~10 s cost → **duty cycle ≈ 1.0 each ⇒ ~20 full-time runner slots** of a 25-slot pool. |
| Head-of-line blocking | **Real.** Fast checks are starved by slot occupancy, not by ordering: `http` (cost 42 ms) carries `delay_ewma_ms` ≈ 13 000; `tcp`/`dns`/`udp` 22 000–41 000. Soft WFQ ordering did not keep fast checks fresh. |
| Worst case | 3 browser jobs spiralled to ~95-min offsets and were stranded unclaimable (the defect fixed by `2026-07-01-02`). |

The structural argument (from `2026-07-01-01`): **ordering assigns free slots;
it cannot free occupied ones.** Once slow probes hold every runner goroutine,
no `ORDER BY` helps a fast check — with 3 runners and 3 in-flight slow probes,
a due 50 ms check waits for a slow probe to finish, full stop. The only
admission-side remedy is reserved capacity per class. That is this spec:
re-home the deleted in-memory admission cap (commit `419816ae`) into **storage
(+ the claim query)**, where it is fleet-correct.

Decision confirmed with the user: the lane column is a **`smallint`** (0 =
fast, 1 = slow) rather than a boolean — same semantics today, extensible if a
third class is ever needed, and it matches Phase 4's original design.

## Current state (verified 2026-07-01 on the batch branch; assumes
`2026-07-01-02` has landed — re-verify line numbers at build)

| Concern | Location | Today |
|---|---|---|
| Claim | `checkjobsvc/service.go` `selectAvailableJobs` | one class: gate `scheduled_at ≤ now+maxAhead`, `ORDER BY effective_scheduled_at`, `LIMIT available`; PG `FOR UPDATE SKIP LOCKED`, SQLite optimistic (`updateSingleJobLease` rows-affected check) |
| Claim API | `checkjobsvc.Service.ClaimJobs(ctx, workerUID, region, limit, maxAhead)` | single limit, no class |
| Fetcher | `worker.go` `fetchAndDistributeJobs` (~`:315`) | claims ≤ `availableRunners.Load()` jobs, pushes into one `jobsChan` |
| Runner pool | `worker.go:73-76,437-482` | `poolSize` default **25** (`config.go:552`), `availableRunners atomic.Int32`, no per-class accounting |
| Express path | `worker.go:376-434` + `ClaimJobsForCheck` | own goroutine outside the pool; claims per `check_uid` |
| Post-exec write | `ReleaseLeaseWithSchedulingState` + `worker.go` `releaseLeaseWithCost` | writes cost/delay EWMAs + effective in one UPDATE — the natural home for lane classification |
| Scheduling math | `scheduling/scheduling.go` | pure functions over `Params`; after `2026-07-01-02`: cost-only bounded offset. **No lane classifier** |
| Columns / indexes | migrations 001/006/007/008, `db/models/check_job.go` | `cost_ewma_ms`, `delay_ewma_ms`, `plan_weight`, `effective_scheduled_at` (+ index). **No lane column** |
| Config | `config.go` `SchedulingConfig` (~`:479`) + `applySchedulingEnv` | `slow_threshold_ms` (offset dead-band, default 2000), tier credit, cost-timeout knobs. Multi-word `SP_*` keys need the manual env reader [[project_koanf_env_quirk]] |
| Load harness | `checkers/checksleep/`, `solidping dev browser-to-sleep`, `GET /api/mgmt/scheduling/cost-distribution` | all shipped by `2026-07-01-01` — reuse for verification |
| Metrics | `prommetrics` `RecordClaimJobsOutcome`, `RecordCheckStage` | no per-lane visibility |

## Design decisions

### D1 — Lanes are hard isolation **layered on** WFQ ordering, not a replacement

Unchanged from `2026-07-01-01` D3: the lane gives fast checks a capacity
floor; `effective_scheduled_at` ordering (cost-only after `2026-07-01-02`)
keeps fairness + anti-starvation *within* each lane. Tier credit
(`plan_weight`) continues to apply within lanes.

### D2 — `lane smallint`, derived from **cost only**, with hysteresis

- `check_jobs.lane smallint not null default 0` (0 = fast, 1 = slow).
- Classified in the **existing post-exec write** — no new hot-path query:
  `scheduling.ClassifyLane(prevLane, costEWMAMs, params)` (pure, unit-tested):
  promote to slow at `cost ≥ lane_slow_threshold_ms` (default **2000**), demote
  to fast at `cost < lane_fast_threshold_ms` (default **1000**). The dead-band
  stops a check hovering at the threshold from flipping lanes every run and
  churning the partial indexes. Never classify on `delay_ewma_ms` — delay is a
  victim signal (see `2026-07-01-02`); classifying on it would send starved
  fast checks into the slow lane, the exact inversion of the goal.
- New rows start fast (first run is FIFO; one execution reclassifies).
  `ReleaseLease` (no-sample variant: deferral/reaper/remote) leaves the lane
  unchanged.

### D3 — Reservation with borrowing, enforced per worker at claim time

Per `2026-07-01-01` D4, a static "N fast + M slow" split is rejected as not
work-conserving. Instead, with pool size `P` and a reserved fast floor `F`
(config `scheduling.fast_lane_reserved`, default **5**, clamped to `[0, P−1]`
with a startup warning when clamped):

> **Invariant: slow jobs in flight ≤ P − F on every worker.** Fast jobs may
> occupy any free slot; slow jobs may only occupy slots above the floor. An
> idle slow lane donates everything to fast (trivially); an idle fast stream
> lets slow borrow up to `P − F`; a fast burst never finds fewer than `F` slots
> occupied by nothing slower than another fast check.

Claim per fetch (one transaction, two SELECTs, fast first):

```
free      = availableRunners
slowBudget= max(0, (P − F) − busySlow)          // busySlow = in-flight slow probes
fast[]    = claim(lane=0, limit=free)
slow[]    = claim(lane=1, limit=min(free − len(fast), slowBudget))
```

Per-worker enforcement **is** fleet-correct — every worker independently
guarantees its own floor, so the fleet guarantees ΣF — which is exactly what
the deleted per-worker *in-memory* cap could not do, because without a lane
column it had to claim blind and defer, wasting claims. `SKIP LOCKED` racing
can only under-claim, never breach the floor.

### D4 — `busySlow` accounting lives in the worker, keyed off the claimed row

`busySlow atomic.Int32` on `CheckWorker`: incremented when a runner starts a
job with `lane == 1`, decremented when it finishes (defer). The fetcher reads
it when computing `slowBudget`. The express path bypasses the pool and the
accounting (a freshly created check is lane-fast by definition; its goroutine
is additive). Expose as a gauge for observability.

### D5 — Two partial indexes; benchmark against the composite alternative

Migration (both dialects — SQLite supports partial indexes):

```sql
alter table check_jobs add column lane smallint not null default 0;
update check_jobs set lane = 1 where cost_ewma_ms >= 2000;  -- backfill at the default threshold
create index idx_check_jobs_claim_fast on check_jobs (effective_scheduled_at) where lane = 0;
create index idx_check_jobs_claim_slow on check_jobs (effective_scheduled_at) where lane = 1;
drop index if exists idx_check_jobs_effective_scheduled_at;
```

Number it after `2026-07-01-02`'s migration (expected `009`; renumber to
next-free at build). Run `make bench-checks` partial-vs-composite
(`(lane, effective_scheduled_at)`) before committing to the index shape; keep
whichever wins, document the result in the migration comment.

### D6 — Claim API grows per-lane limits; SQLite path identical

`ClaimJobs(ctx, workerUID, region, fastLimit, slowLimit int, maxAhead)` (or a
small `ClaimSpec` struct if signatures sprawl) running both lane SELECTs inside
the existing transaction, sharing `updateJobsWithLease` + `attachChecks`.
`ClaimJobsForCheck` (express) stays lane-agnostic. `RecordClaimJobsOutcome`
gains a lane dimension (or a new `solidping_check_lane_claims_total{lane}`
counter) so saturation of the slow lane is visible.

## Implementation

1. **Migration 009** (postgres + sqlite): `lane` column, backfill from
   `cost_ewma_ms`, partial indexes, drop the full effective index (per D5
   bench). Down: drop indexes + column, recreate the full index.
2. **Model**: `Lane int16` on `db/models/check_job.go`; `NewCheckJob` → 0.
3. **Scheduling math**: `ClassifyLane` + `LaneFast`/`LaneSlow` consts +
   hysteresis params in `scheduling.Params`; table-driven tests
   (promote/demote/hold, zero-cost fresh job stays fast, thresholds inverted →
   startup validation error).
4. **Config**: `scheduling.lane_slow_threshold_ms`, `lane_fast_threshold_ms`,
   `fast_lane_reserved` + defaults; extend `applySchedulingEnv` for the
   multi-word `SP_*` forms [[project_koanf_env_quirk]]; validate
   `fast < slow`, clamp `F` to `[0, P−1]`.
5. **checkjobsvc**: per-lane claim (D6); post-exec write persists the new lane
   (`ReleaseLeaseWithSchedulingState` gains the lane argument).
6. **Worker**: `releaseLeaseWithCost` computes `newLane = ClassifyLane(...)`;
   `busySlow` accounting (D4); `fetchAndDistributeJobs` computes
   `fastLimit`/`slowLimit` (D3). Remove the now-stale "admission caps" comment
   at `worker.go:100-102`.
7. **Metrics**: lane label on claim outcomes; `busySlow` gauge (extend
   `newWorkerChannelCollector`).

## Out of scope

- More than two lanes, preemption of in-flight probes, a true global WFQ
  virtual clock (unchanged from `2026-06-30-09` / `2026-07-01-01`).
- Per-**org** fairness inside a lane (one org's 20 slow checks can still crowd
  another org's slow checks) — adjacent to
  `specs/backlog/2026-03-30-org-check-rate-limit.md`, not this spec.
- Auto-tuning the thresholds or the floor.
- Reducing the offered slow load itself — `2026-07-01-04`.

## Verification

```bash
make build && make lint     # [[feedback_lint_strict]]
make test
make migrate                # fresh + populated + stale dev DBs, both dialects [[project_migration_consolidation_stale_db]]
make bench-checks           # partial vs composite index; claim path must not regress
```

- **Floor invariant (the core test, use `checksleep`):** pool `P=4`, `F=1`,
  saturate with slow sleep jobs (e.g. `sleep_ms` 3000, 10 s period), add due
  fast jobs → assert fast jobs are claimed while ≥ 3 slow are in flight, and
  in-flight slow never exceeds `P − F`. Symmetric: no fast work due → slow
  claims reach exactly `P − F` (borrowing works). Keep sleeps ms-scale
  [[feedback_flaky_tests_are_bugs]].
- **Hysteresis:** a job oscillating 900↔2100 ms cost flips lanes only across
  the band edges; one hovering at 1500 ms holds its lane.
- **Claim ordering within a lane** still follows `effective_scheduled_at`
  (existing WFQ tests extended with a lane filter).
- **Migration:** backfill puts the dev instance's 20 browser + mongodb jobs in
  lane 1, everything < 2 s in lane 0.
- **Manual (dev instance):** re-run the `2026-07-01-01` experiment — ~90% fast
  / 10% slow-timeout mix via sleep checks, saturate, and confirm via
  `GET /api/mgmt/scheduling/cost-distribution` that the fast cohort's
  `delay_ewma_ms` now stays near zero where it previously inflated. That
  before/after is the acceptance criterion for the whole spec.

## Key files

| File | Change |
|---|---|
| `server/internal/db/{postgres,sqlite}/migrations/009_check_job_lane.{up,down}.sql` | **+** lane, backfill, partial indexes |
| `server/internal/db/models/check_job.go` | **~** `Lane` field |
| `server/internal/checkworker/scheduling/scheduling.go` (+`_test`) | **~** `ClassifyLane`, hysteresis params |
| `server/internal/checkworker/checkjobsvc/service.go` (+`_test`) | **~** per-lane reservation claim; lane in post-exec write |
| `server/internal/checkworker/worker.go` (+`_test`) | **~** `busySlow`, per-lane fetch limits, lane into release |
| `server/internal/config/config.go` | **~** `lane_*` / `fast_lane_reserved` keys + env reader + validation |
| `server/internal/prommetrics/metrics.go` | **~** lane dimension + gauge |

## Risk log

| Risk | Mitigation |
|---|---|
| Floor misconfigured (`F ≥ P`) silently kills the slow lane | Clamp to `P−1` + startup warning; config validation test. |
| Lane flapping churns partial indexes | Hysteresis band (D2); flap test. |
| Two-SELECT claim slows the hot path | Same transaction, both SELECTs `LIMIT`-bounded and index-backed; `make bench-checks` gate. |
| Slow lane saturates and falls behind | **Intended, contained failure mode**: slow checks degrade to best-effort at their offered load while fast checks stay on time. Surfaced via lane claim metrics + cost-distribution endpoint; demand-side fix is `2026-07-01-04`. |
| SQLite behavior diverges (no `SKIP LOCKED`) | Reuses the existing optimistic-locking release path unchanged; lane filter is dialect-neutral SQL. |
| Backfill misclassifies at migration time | Threshold matches the default config; one run reclassifies via hysteresis anyway. |

**Status**: Todo | **Created**: 2026-07-01 | **Depends on**: `2026-07-01-02` | **Extends**: `2026-07-01-01` (Phase 4 — gate answered GO) & `2026-06-30-09`

## Implementation Plan

1. **Migration 009** — `server/internal/db/{postgres,sqlite}/migrations/009_check_job_lane.{up,down}.sql`:
   `lane smallint not null default 0`, backfill `lane = 1 where cost_ewma_ms >= 2000`, partial
   indexes `idx_check_jobs_claim_fast` (`where lane = 0`) / `idx_check_jobs_claim_slow`
   (`where lane = 1`) on `effective_scheduled_at`, drop `idx_check_jobs_effective_scheduled_at`.
   Down: drop partial indexes + lane, recreate the full index. Extend
   `sqlite/migrations_test.go` (lane column + default, new indexes present, old index gone,
   backfill statement classifies `cost >= 2000` → 1) and fix
   `TestMigrationCheckJobSchedulingColumns` which asserts the dropped index. The
   partial-vs-composite `make bench-checks` comparison runs at QA time; the winner + numbers are
   documented in the migration comment before merge (D5).
2. **Model** — `Lane int16 bun:"lane,notnull,default:0"` on `db/models/check_job.go`;
   `NewCheckJob` keeps the zero value (fast).
3. **Scheduling math** — `scheduling.go`: `LaneFast`/`LaneSlow` consts,
   `ClassifyLane(prevLane int16, costEWMAMs float64, p Params) int16` with hysteresis
   (promote at `cost ≥ LaneSlowThresholdMs`, demote below `LaneFastThresholdMs`, hold in the
   band; `LaneSlowThresholdMs ≤ 0` = classifier off → hold). Params gains both thresholds.
   Table-driven tests: promote/demote/hold, zero-cost fresh job stays fast, 900↔2100 flap test,
   1500 holds lane, classifier-off.
4. **Config** — `SchedulingConfig` gains `lane_slow_threshold_ms` (default 2000),
   `lane_fast_threshold_ms` (default 1000), `fast_lane_reserved` (default 5);
   `applySchedulingEnv` reads the `SP_SCHEDULING_*` forms (koanf multi-word quirk);
   `Config.Validate()` rejects inverted/negative thresholds (`fast < slow`). Clamping of `F`
   to `[0, P−1]` + startup warning lives in `NewCheckWorker` (it knows `P`). Tests for env
   reader + validation + clamp.
5. **checkjobsvc** — `ClaimJobs(ctx, workerUID, region, fastLimit, slowLimit int, maxAhead)`:
   one transaction, two `SELECT`s (fast first, `lane = 0` limit `fastLimit`; then `lane = 1`
   limit `min(fastLimit − claimedFast, slowLimit)`), sharing `updateJobsWithLease` +
   `attachChecks`. `ReleaseLeaseWithSchedulingState` gains `lane int16` and persists it.
   `ClaimJobsForCheck` (express) unchanged. Adapt the two lane-agnostic callers
   (`backend/direct.go`, `handlers/workers/service.go`) to pass `slowLimit = limit`
   (no reservation on the remote-claim path). Tests: fast-first, slow bounded by `slowLimit`,
   slow bounded by remaining capacity, per-lane `effective_scheduled_at` ordering, lane
   persisted on release.
6. **Worker** — `busySlow atomic.Int32` (incremented in the fetcher right after a successful
   claim, keyed off the claimed row's lane — closes the fetch-race window; decremented by the
   runner when the job finishes); pure helper `laneLimits(free, poolSize, reserved, busySlow)`
   implementing `slowBudget = max(0, (P−F) − busySlow)`; `fetchAndDistributeJobs` uses it;
   `releaseLeaseWithCost` computes `newLane = ClassifyLane(...)`; replace the stale
   "admission caps" comment in `NewCheckWorker`. Floor-invariant test with `checksleep`:
   `P=4, F=1`, saturate with slow sleep jobs, then add due fast jobs → fast complete while ≥3
   slow in flight (fast results land before the first slow result), sampled `busySlow` never
   exceeds `P−F` and reaches exactly `P−F` (borrowing). ms-scale sleeps.
7. **Metrics** — `solidping_check_lane_claims_total{lane}` counter (`RecordLaneClaims`),
   recorded per claim batch in the fetcher; `solidping_worker_busy_slow` gauge added to
   `newWorkerChannelCollector`.
8. **QA** — `make build-backend lint-back test`, `make migrate` (dev DB), `make bench-checks`
   partial-vs-composite comparison + no-regression run; document the D5 decision in the
   migration comment.
