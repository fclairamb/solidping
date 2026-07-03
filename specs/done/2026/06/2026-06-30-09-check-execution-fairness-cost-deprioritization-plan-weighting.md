# Check-execution fairness: de-prioritize slow checks, and impact paid plans less than free

## Context

Two related scheduling problems, both rooted in the same place — the worker
dispatch path:

1. **Slow checks delay everyone (head-of-line blocking).** A check that is slow
   to execute (a timing-out endpoint, a slow TLS handshake) holds a scarce
   runner slot for up to its full 30s timeout. When enough slow checks coincide,
   the runner pool saturates, the fetcher stops claiming, and `scheduled_at`
   drifts behind `now` for **every** check — including the healthy 200 ms ones.
   We want to **de-prioritize checks that are consuming too much time** so their
   cost does not become everyone else's latency.

2. **Paid plans should be impacted *less* than free plans.** Whatever
   de-prioritization / contention-shedding we apply, a free-tier stampede must
   not degrade a paid org's monitoring freshness. Paid plans should get a larger,
   protected share of execution capacity.

This spec designs a **cost-aware, plan-weighted scheduler** layered onto the
existing pull-based job queue, plus the capacity-isolation primitive that
ordering alone cannot provide.

> **Not to be confused with** `2026-06-30-07-adaptive-recovery-flap-backoff-redesign`.
> That spec backs off the **incident state machine** (how long a flapping check
> must be stable before auto-resolving). This spec backs off **execution
> scheduling** (when/whether a check runs at all, and in what order). Different
> layer, different mechanism — they do not interact.

---

## Current state (verified against source)

| Concern | Location | Today |
|---|---|---|
| Claim query | `checkworker/checkjobsvc/service.go:215–221, 260–266` | `WHERE scheduled_at <= now(+maxAhead)` … **`ORDER BY scheduled_at ASC`**; Postgres `FOR UPDATE SKIP LOCKED`, SQLite optimistic retry (`:279`) |
| Reschedule after run | `checkjobsvc/service.go:380` | `Set("scheduled_at = ?", nextScheduledAt)` — single post-exec write (a place to fold cheap state updates into) |
| Lease window | `checkjobsvc/service.go:324` | `max(scheduled_at, now) + period + 30s` |
| Runner pool | `checkworker/worker.go:67–69, 86–88` | `poolSize = cfg.Server.CheckWorker.Nb` (config default **`nb: 3`**, `config.go:514`; hard fallback **5**, `worker.go:88`). One `jobsChan`, `availableRunners atomic.Int32` |
| Fetcher gate | `worker.go:219, 267–268, 305` | `available := availableRunners.Load()`; claims **at most `available`** jobs; if 0, claims nothing → **queue stalls, schedule drifts** |
| Runner loop | `worker.go:387–411` | `availableRunners.Add(+1)` waiting, `Add(-1)` on pickup — the only concurrency bound |
| Execution timeout | `worker.go:543–564` | **flat 30 s** (`context.WithTimeout(context.Background(), 30*time.Second)`); `DeadlineExceeded` → `StatusTimeout` |
| Per-org rate limit | `worker.go:520–541`; `entitlements/service.go` | `ReserveCheckExecution(org)` token bucket (`MaxChecksPerMinute`), checked **at execution time**; on `QuotaError` → `releaseLease` (reschedules next period, **no result written**), else fail-open |
| Entitlements model | `db/models/org_entitlements.go`; `entitlements/defaults.go` | `Payload.Limits.MaxChecksPerMinute *int` (nil = unlimited) + display-only plan identity (`displayName`, `displayEmoji`) |
| Cost signal | `db/models` (results) | `results.duration float32` + `status` (5=timeout, 6=error) recorded per raw result — **available but not aggregated onto the job** |
| Priority / tier in scheduling | — | **None.** No priority/weight/tier column; all orgs and checks scheduled identically. Rate limit is the only tenant guard, and it is tier-blind FIFO. |
| Backoff on slow/failed | — | **None.** Immediate reschedule next period regardless of cost. |
| Migrations | `db/{postgres,sqlite}/migrations/` | last is **`005_adaptive_recovery`**; next is **`006_…`**, added to **both** dialects |

**Diagnosis.** The work is almost pure network I/O — a goroutine parked on a slow
socket costs ~KB and zero CPU. So `nb: 3–5` is an *artificially scarce* budget
that **manufactures** the head-of-line blocking: 3–5 slow checks = 100 %
occupancy = total stall. The single global FIFO pool has no notion of cost or
tenant, so a slow free-tier check and a fast paid check are indistinguishable to
the scheduler.

---

## Design decisions

### D1 — WFQ ordering and capacity lanes are **both** needed (not either/or)

They fix different failure modes at different layers, and neither subsumes the
other:

- **`effective_scheduled_at` / WFQ ordering** governs the **queue** — *which*
  due job is claimed next, in what order. Property of the claim `SELECT`.
- **Admission caps / lanes** govern the **pool** — who may be *executing
  simultaneously*. Property of the runner capacity.

The hole ordering alone leaves: during a lull only slow checks are due, so they
get claimed and **occupy every slot for 30 s**; *then* a burst of fast checks
becomes due and finds **no free runner**. Ordering is irrelevant once capacity is
committed — execution here is non-preemptible (cancelling the context aborts the
probe). Only a **capacity partition** guarantees a fast/paid burst always finds a
free slot. **Decision: ship WFQ ordering for fine-grained priority + an admission
cap for the hard isolation floor.** The same split applies to paid-vs-free:
weighted ordering makes paid *usually* first; a reserved-slot cap makes paid
*guaranteed* served.

### D2 — gate on real `scheduled_at`, **order** by `effective_scheduled_at`

`effective_scheduled_at = scheduled_at + cost_penalty − tier_credit`, a single
indexable scalar. Two ways to use it:

> **Option A (recommended) — gate on `scheduled_at`, order by `effective`.**
> `WHERE scheduled_at <= now ORDER BY effective_scheduled_at ASC`. De-prioritization
> **only bites under contention**: with spare capacity every check still runs on
> time; only when the due-batch exceeds capacity do cheap/paid checks win the
> slots. This is the minimal-harm reading of "don't let slow checks delay others."
>
> **Option B — gate *and* order on `effective`.** `WHERE effective_scheduled_at <= now`.
> Simpler/indexes cleanly, but **always** delays slow checks by their penalty even
> on an idle pool — a guaranteed latency tax, not contention-only. This is really
> "stretching lite."

**Recommendation: A.** Anti-starvation falls out for free *iff the penalty is
capped*: `effective` stays anchored to the absolute `scheduled_at`, so a
repeatedly-skipped check's `scheduled_at` recedes into the past and eventually
sorts ahead of fresh work. Indexing: add `(effective_scheduled_at)` (or composite
`(scheduled_at, effective_scheduled_at)`); the due set is small, so the sort is
cheap. Benchmark with `make bench-checks`.

### D3 — this is **WFQ-flavored**, not textbook WFQ

True WFQ keeps a per-flow virtual clock and serves smallest-virtual-finish first.
In a pull-based, multi-worker, DB-backed queue that clock is shared mutable state
updated on every claim → contention. We **approximate** it with the per-job
scalar above: cost pushes later, weight pulls earlier, the absolute anchor
provides aging. Honest naming in the spec/UI: "cost-aware, plan-weighted
deadline scheduling." A real global virtual-clock is **out of scope**.

### D4 — "paid less impacted" is the **same** mechanism, parameterized by tier

Not a separate code path. Every Q1 knob (slow threshold, penalty size, stretch
trigger, cost-timeout aggressiveness) reads its parameters from the org's
**plan weight** (derived from `org_entitlements`): free = aggressive, paid =
gentle. Plus two tier-specific guarantees: **reserved paid capacity** (a hard
floor) and **free-shed-first** under sustained overload.

### D5 — distributed caveat (be honest)

`availableRunners` is **per-worker**. Admission caps and reserved capacity are
therefore enforced **per-worker**; with homogeneous workers a per-worker fraction
approximates a global reservation, but it is not a strict global guarantee. A
coordinated global reservation needs cross-worker state — **out of scope**; noted
in the risk log.

---

## Recommended implementation (phased)

Each phase is independently shippable and independently valuable. **Phase 1 alone
answers both questions at a basic level.**

### Phase 1 — capacity isolation (highest value, smallest change)

The cheapest fix for both problems: stop a small homogeneous pool from being the
bottleneck, and partition it.

1. **Right-size + justify the pool.** I/O-bound work tolerates far more
   concurrency than `nb: 3`. Raise the default and document that the *real* bound
   is now the per-class/per-org caps (below) and DB-flush throughput, not the
   goroutine count. Keep `nb` tunable (`config.go:407–409`).
2. **Cost signal on the job.** Add `cost_ewma_ms` to `check_jobs`; update it in
   the **existing** post-exec write (`checkjobsvc/service.go:380`) —
   `cost_ewma_ms = α·duration + (1−α)·cost_ewma_ms`, timeouts (status 5) counted
   as the ceiling. **No new query on the hot path.**
3. **Slow-lane admission cap.** Maintain a second counter
   `availableSlowRunners` (cap `K < poolSize`). A runner picking a
   slow-classified job (`cost_ewma_ms > slow_threshold` OR recent timeout) must
   also hold a slow-permit; if none, `releaseLease` and let it be reclaimed.
   Guarantees ≥ `poolSize − K` slots always free for fast checks.
4. **Reserved paid capacity.** Denormalize `plan_weight smallint` onto
   `check_jobs` (from entitlements). Cap free-tier in-flight at `poolSize − R`,
   reserving `R` slots claimable only by paid jobs. This is the Q2 hard floor.
5. Wire the fetcher (`worker.go:267–268`) to respect both caps when choosing how
   many of each class to claim.

### Phase 2 — cost-awareness

6. **Cost-aware execution timeout.** Replace flat 30 s (`worker.go:546`) with
   `clamp(k · cost_ewma_ms, floor, 30s)` — a 200 ms-p95 check no longer reserves
   30 s of worst-case occupancy; chronic 30 s offenders stand out. Generous `k`
   and `floor` so high-variance checks are not newly failed; 30 s stays the
   ceiling. Per-check override later.
7. **Scheduling-lag metric + degraded mode.** Add a gauge for how far the median
   claimed `scheduled_at` trails `now`. When it exceeds a threshold (sustained
   overload), enter a bounded degraded mode that **temporarily stretches
   free-tier periods** to shed load and protect paid freshness; auto-recover.
   (Companion to the existing `ChecksRateLimited` metric, `worker.go:528`.)

### Phase 3 — WFQ ordering + interval stretching (full fairness)

8. **`effective_scheduled_at` column + ordering.** Compute
   `scheduled_at + cost_penalty(cost_ewma) − tier_credit(plan_weight)`, **capped**
   (anti-starvation). Change the claim `ORDER BY` to `effective_scheduled_at ASC`
   (keep the `scheduled_at` gate — D2/Option A). Add the index; benchmark.
9. **Interval stretching for chronic offenders.** When `cost_ewma_ms` approaches
   `period` (the check is structurally expensive — e.g. a 30 s-timeout check on a
   30 s interval continuously holds a slot), stretch its effective period
   (`next = now + period · stretch_factor`), `stretch_factor` capped and reset
   when cost drops. **Asymmetric by tier** (D4): free stretches sooner/harder,
   paid later/gentler or not at all. This is the only lever that *reduces* load.
10. **Surface it.** Stretching changes how often we monitor a customer endpoint —
    a product-visible decision. Show it in dash0 ("monitored less frequently —
    slow responses") per the design-reference rule; never silently.

### Config (all tunable; no redeploy)

New koanf keys / system parameters (follow the param-key convention, mind the
multi-word `SP_*` env quirk [[project_koanf_env_quirk]]): e.g.
`scheduling.slow_cost_threshold_ms`, `scheduling.slow_lane_max`,
`scheduling.paid_reserved`, `scheduling.penalty_cap_seconds`,
`scheduling.tier_credit_seconds`, `scheduling.cost_timeout_factor`,
`scheduling.cost_timeout_floor_ms`, `scheduling.stretch_max`,
`scheduling.lag_degrade_seconds`.

---

## Out of scope

- **Preemption of running checks.** Isolation is via *admission*, never by
  cancelling an in-flight probe.
- **True global WFQ virtual-clock** across workers (D3) and **strict global**
  reserved capacity (D5) — per-worker approximation only.
- **Incident-state flapping backoff** — `2026-06-30-07`.
- **Manual per-check priority UI.** Weight is tier-derived first; a user-facing
  priority knob is a possible follow-up.
- **Per-tier multi-region split multipliers** (`reconcileCheckJobs`,
  `splitPeriod = basePeriod × numRegions`) — a related future lever, not here.

---

## Verification

```bash
make dev-test       # backend + dash0 on :4000
make migrate        # apply 006 on fresh AND populated DB, both SQLite + Postgres
make bench-checks   # claim-path throughput must not regress (esp. new ORDER BY)
```

- **Migration (both dialects):** existing `check_jobs` get `cost_ewma_ms=0`,
  `plan_weight` from entitlements (default free), `effective_scheduled_at`
  backfilled to `scheduled_at`. Verify on stale dev DBs
  [[project_migration_consolidation_stale_db]].
- **Cost EWMA / classification** (unit): duration feeds EWMA; timeouts pin the
  ceiling; threshold flips `slow`; no extra per-result query (fold into `:380`).
- **Admission cap** (scheduler test): inject a backlog of slow jobs → assert
  **never more than `K` slow in flight** and that fast jobs are always claimable.
- **Reserved paid** (scheduler test): free-tier stampede + paid jobs → assert
  free in-flight never exceeds `poolSize − R`; paid claimed promptly.
- **WFQ ordering** (unit + scheduler): under contention, fast & paid claimed
  before slow & free; `effective` capped; a long-skipped check eventually wins
  (no starvation). With spare capacity, **everyone runs on time** (Option A).
- **Cost-aware timeout:** a 200 ms check gets a short ceiling; a high-variance
  check is **not** newly timed out (floor/`k` generous).
- **Stretching:** a check whose cost ≈ period gets a capped longer effective
  period; resets when fast again; **paid is gentler than free**; UI shows it.
- **Manual:** point HTTP checks at a 30 s-sink across a **free** and a **paid**
  org → paid stays on schedule, free slips; healthy checks unaffected.
- `make test`, `make lint` (no new findings — never relax config
  [[feedback_lint_strict]]), `make test-dash`; e2e for the stretched-check UI;
  prefer Playwright [[feedback_browser_testing]], treat flakes as bugs
  [[feedback_flaky_tests_are_bugs]].

---

## Key files

| File | Change |
|---|---|
| `server/internal/db/{postgres,sqlite}/migrations/006_*.{up,down}.sql` | **+** `check_jobs`: `cost_ewma_ms`, `plan_weight`, `effective_scheduled_at` (+ index); both dialects |
| `server/internal/db/models/check_job.go` | **~** new fields + zero-defaults |
| `server/internal/checkworker/checkjobsvc/service.go` | **~** claim `ORDER BY effective_scheduled_at` (keep `scheduled_at` gate); per-class/per-tier claim caps; fold `cost_ewma_ms`/`effective_scheduled_at` into the `:380` post-exec write |
| `server/internal/checkworker/worker.go` | **~** `availableSlowRunners` + paid-reserve in fetcher (`:267`) & runner loop (`:387`); cost-aware timeout (`:546`); slow/tier classification; scheduling-lag gauge |
| `server/internal/config/config.go` | **~** `scheduling.*` keys; revisit `CheckWorker.Nb` default |
| `server/internal/entitlements/` | **~** expose `plan_weight` from entitlements; denormalize onto jobs on entitlement change + reconcile |
| `server/internal/handlers/checks/service.go` | **~** set `plan_weight` when materializing/reconciling jobs (`reconcileCheckJobs`) |
| `web/dash0/src/…` (check view) | **+** "monitored less frequently (slow)" indicator; i18n `{en,fr,de,es}` |
| `server/internal/app/openapi/openapi.yaml` | **~** if cost/stretch state is surfaced via API |

---

## Risk log

| Risk | Mitigation |
|---|---|
| Cost-aware timeout newly fails high-variance checks | Generous `k` + `floor`; 30 s stays the ceiling; per-check opt-out. |
| Stretching reduces monitoring frequency → missed outage on a slow endpoint | Cap `stretch_factor`; reset on recovery; **gentler/none for paid**; **surface in UI**; fully configurable. |
| New `ORDER BY effective_scheduled_at` regresses the hot claim path | Index it; keep the `scheduled_at` gate; `make bench-checks` before/after. |
| Starvation of a permanently-slow / free check | **Cap** the penalty; `effective` anchored to absolute `scheduled_at` ⇒ recedes into the past ⇒ eventually wins. Degraded mode is bounded + temporary. |
| `plan_weight` denormalization goes stale on plan change | Refresh on entitlement update (reconcile path) + periodic reconcile job. |
| Per-worker caps ≠ strict global guarantee (D5) | Homogeneous-worker fraction approximates it; document the limitation; coordinated global reservation is a follow-up. |
| Rate-limit `releaseLease` defers tier-blind FIFO today | Fold tier/cost into the deferral so paid is not reshuffled behind free. |
| Migration on both dialects; stale dev DBs skip it | Add to both; reset/patch dev DB; verify [[project_migration_consolidation_stale_db]]. |
| Reintroducing scheduler complexity | Phase 1 (caps) ships value alone; Phases 2–3 are optional escalations; every knob defaults to today's behaviour (penalty/credit/stretch = 0, caps = poolSize). |

**Status**: Todo | **Created**: 2026-06-30 | **Related**: `2026-06-30-07-adaptive-recovery-flap-backoff-redesign.md`

---

## Implementation Plan

1. **Phase 1 — isolation.** Migration `006` (cost/weight/effective columns, both
   dialects); `cost_ewma_ms` in the post-exec write; `availableSlowRunners` slow
   cap; paid-reserved cap; `plan_weight` denormalization. Scheduler tests for both
   caps. **Ships the core answer to both questions.**
2. **Phase 2 — cost-awareness.** Cost-aware timeout; scheduling-lag gauge +
   bounded free-shed degraded mode.
3. **Phase 3 — WFQ + stretching.** `effective_scheduled_at` ordering (Option A) +
   index + bench; capped penalty/credit; asymmetric interval stretching; dash0
   indicator + i18n.
4. **QA.** `make test` / `make lint` / `make test-dash` / `make bench-checks`;
   manual slow-sink free-vs-paid scenario; both-dialect migration on fresh +
   populated DBs.

> **Open decisions to confirm before building:** (a) D2 Option A (contention-only)
> vs B (always-tax); (b) default `poolSize` and the `K`/`R` caps; (c) whether paid
> is *exempt* from stretching or merely gentler; (d) the free-shed degraded-mode
> trigger threshold.
