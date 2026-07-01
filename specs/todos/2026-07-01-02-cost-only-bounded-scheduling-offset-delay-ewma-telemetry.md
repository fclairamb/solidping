# Cost-only, structurally bounded scheduling offset — delay EWMA becomes pure telemetry

## Context

Spec `2026-06-30-09` introduced WFQ-style claim ordering for `check_jobs`:

```
effective_scheduled_at = scheduled_at + offset − tier_credit
offset                 = delay_ewma_ms + cost_ewma_ms × CostOffsetWeight   (dead-band: SlowThresholdMs)
```

Live measurement on the dev instance (2026-07-01, `server/solidping.db`, 74 jobs
— the deferred Phase-3 measurement of spec `2026-07-01-01`) shows the **delay
term in the offset is actively harmful**, in two distinct ways:

1. **It punishes victims, not offenders.** Cost is an *offender* signal (an
   expensive check occupies a runner slot); delay is a *victim* signal (the
   check started late because the pool was busy). Folding delay into the offset
   pushes a fast check that was already starved *further back* on its next
   cycle. Measured: `http` checks with `cost_ewma_ms ≈ 42` carry
   `delay_ewma_ms ≈ 13 000` → a 13 s deprioritization for a 42 ms check that
   did nothing wrong.
2. **It is unbounded and spirals under sustained overload.** Under overload the
   probe misses even its padded deadline, so the delay sample stays positive,
   the EWMA grows, the deadline is padded more, and so on — unbounded positive
   feedback. Measured: three `browser` jobs reached `delay_ewma_ms ≈ 5 700 000`
   (**~95 min offset ≈ 19 × the 5-min claim window**) and were observed
   **stranded**: >680 s overdue, unclaimed, unclaimable — the "absolute
   `scheduled_at` anchor" anti-starvation argument fails because the offset
   grows faster than `scheduled_at` recedes. Recovery by EWMA decay
   (α = 0.3 → ×0.7/run) takes hours.

The current batch already widened the effective claim gate by
`effectiveWindowFactor = 12` (`checkjobsvc/service.go:29`) to make heavily
penalized jobs claimable. That treats the **symptom** (unclaimability); the
three stranded rows above exceed even the widened window (95 min > 12×5 min).
This spec removes the **cause**: with delay out of the offset, the offset is
*structurally* bounded by `2 × DefaultExecutionTimeout = 60 s`, because
`cost_ewma_ms` is pinned to the 30 s execution ceiling on timeout. The factor-12
widening then becomes dead weight and is deleted.

`delay_ewma_ms` itself **stays** — it is the head-of-line measurement that
gates the lane split (spec `2026-07-01-03`) and the metric behind
`GET /api/mgmt/scheduling/cost-distribution`. It just stops steering the queue,
and its reference point changes so it means what an operator thinks it means:
*how late did the probe start vs the schedule the user configured*.

## Current state (verified against working tree 2026-07-01; the checkworker
package is active on this batch branch — re-verify line numbers at build)

| Concern | Location | Today |
|---|---|---|
| Offset formula | `scheduling/scheduling.go:101-120` (`EffectiveScheduledAt`, `deprioritizeOffset`) | `delay + 2×cost`, dead-band `SlowThresholdMs` (default 2000, `config.go:560`), **no upper bound** |
| Cost weight | `scheduling/scheduling.go:31` | `CostOffsetWeight = 2` |
| Delay sample | `worker.go:737-752` (`delaySampleMs`) | `max(0, execStart − effective_scheduled_at)` — measured against the *padded* deadline, so the sample under-reports true lateness once the offset absorbs it |
| EWMA update | `scheduling/scheduling.go:162-177` (`EWMAAlpha = 0.3`, `UpdateEWMA`) | shared by cost + delay |
| Claim gate & order | `checkjobsvc/service.go:279-333` (`selectAvailableJobs`) | gate `effective ≤ now + maxAhead×12` **and** `scheduled_at ≤ now + maxAhead`; `ORDER BY effective_scheduled_at` |
| Gate widening | `checkjobsvc/service.go:29` (`effectiveWindowFactor = 12`) + test `TestClaimAdmitsHeavilyPenalizedEffective` | added this batch, uncommitted at spec time — superseded by this spec |
| Post-exec write | `checkjobsvc/service.go:440-463` (`ReleaseLeaseWithSchedulingState`), `worker.go:715-729` (`releaseLeaseWithCost`) | folds cost + delay EWMAs + recomputed effective into the one release UPDATE |
| Cost pinning | `worker.go:703-709` (`costSampleMs`) | timeout → pinned to `DefaultExecutionTimeout` (30 s) → `cost_ewma_ms ≤ ~30 000` |
| Columns | migrations 006 + 007, `db/models/check_job.go` | `cost_ewma_ms`, `delay_ewma_ms`, `plan_weight`, `effective_scheduled_at` + index |
| Polluted rows | live DBs migrated through 006/007 | `effective_scheduled_at` values carry delay-inflated offsets (up to ~95 min observed) |

## Design decisions

### D1 — The offset is computed from cost only

`offset = cost_ewma_ms × CostOffsetWeight`, still subject to the
`SlowThresholdMs` dead-band (below it, offset = 0). Delay is removed from
`EffectiveScheduledAt` (signature loses its `delayEWMAMs` parameter).
Deprioritization becomes a pure *offender* penalty: with the measured data,
`browser` sorts +20 s, `mongodb` +60 s, every fast check sorts at its real
`scheduled_at`. Chronically-late-but-cheap checks are no longer pushed back —
that behavior change is the point.

### D2 — An explicit invariant clamp, even though the bound is now structural

`cost_ewma_ms` is pinned to the execution ceiling, so the offset is naturally
≤ `2 × 30 s = 60 s` ≪ the 5-min claim window. Still, clamp in
`deprioritizeOffset`:

```go
const MaxDeprioritizeOffset = 2 * DefaultExecutionTimeout // 60s, = CostOffsetWeight × ceiling
```

so no future term added to the offset can ever reintroduce the stranding
pathology. A unit test asserts the invariant for absurd inputs (e.g.
`cost = 10⁹`).

### D3 — The claim gate returns to `scheduled_at`; ordering stays on `effective_scheduled_at`

This is migration 006's original D2/Option A design ("gate on `scheduled_at`,
order by `effective`"), which the implementation drifted from. With the offset
bounded at 60 s there is no reason to gate on the effective time at all:

- gate: `scheduled_at <= now + maxAhead` (+ lease predicate, unchanged)
- order: `ORDER BY effective_scheduled_at ASC`

Delete `effectiveWindowFactor` and the double gate. Rework the batch's
`TestClaimAdmitsHeavilyPenalizedEffective` into the stronger assertion: a due
job is claimable **immediately** regardless of any stored `effective` value.
Keep `idx_check_jobs_effective_scheduled_at` for the sort; `make bench-checks`
confirms the due-set sort stays cheap.

### D4 — `delay_ewma_ms` is retained as honest telemetry

- Keep the column, the EWMA fold, and the `ReleaseLeaseWithSchedulingState`
  signature (still writes both EWMAs in the single post-exec UPDATE).
- Change `delaySampleMs` to measure against **`scheduled_at`** (fall back
  unchanged when nil): `max(0, execStart − scheduled_at)`. Since delay no
  longer feeds the offset there is no feedback loop, and the metric becomes
  "true start lateness vs the user-configured schedule" — exactly what the
  lane-split go/no-go (spec `2026-07-01-03`) and the cost-distribution endpoint
  need. Update the field docs in migration comments (comment-only, no schema
  change) is **not** required; update the `openapi.yaml` description of
  `delayEwmaMs` percentiles in the cost-distribution response.

### D5 — Migration heals polluted rows

Migration `008` (both dialects; renumber to next-free at build time):

```sql
update check_jobs set effective_scheduled_at = scheduled_at
where effective_scheduled_at > scheduled_at; -- delay-era offsets; cost offset reapplies on next release
```

No new columns. This un-strands the ~95-min rows fleet-wide in one statement;
the cost-only offset repopulates on each job's next release. Down-migration is
a no-op (comment why: the old values are unrecoverable and were wrong).

## Implementation

1. `scheduling/scheduling.go`: drop `delayEWMAMs` from `EffectiveScheduledAt` /
   `deprioritizeOffset`; add `MaxDeprioritizeOffset` clamp; update package doc
   (delay is telemetry-only). Adjust `scheduling_test.go` (ordering, dead-band,
   clamp-invariant tests).
2. `worker.go`: `releaseLeaseWithCost` computes `effective` from cost only;
   `delaySampleMs` references `scheduled_at`.
3. `checkjobsvc/service.go`: single `scheduled_at` gate; delete
   `effectiveWindowFactor`; keep `ORDER BY effective_scheduled_at`. Adjust
   `service_test.go` (incl. the reworked heavily-penalized-job test; keep the
   WFQ-ordering tests — they should still pass with cost-only offsets).
4. Migration `008` postgres + sqlite (up = heal UPDATE, down = no-op).
5. `openapi.yaml`: clarify `delayEwmaMs` semantics on the cost-distribution
   response.

## Out of scope

- The fast/slow lane split and any claim-capacity reservation —
  `2026-07-01-03`.
- Product-level period/duty-cycle admission — `2026-07-01-04`.
- Removing `delay_ewma_ms` or its EWMA machinery (it is the measurement
  instrument).
- Tier credit (`plan_weight`) semantics — unchanged.

## Verification

```bash
make build && make lint   # no new findings; never relax config [[feedback_lint_strict]]
make test                 # scheduling + checkjobsvc suites
make migrate              # 008 on fresh AND populated DBs, both dialects [[project_migration_consolidation_stale_db]]
make bench-checks         # claim path: no regression from the gate change
```

- Unit: offset is cost-only; dead-band respected; `MaxDeprioritizeOffset`
  invariant holds for absurd cost/delay inputs; `EffectiveScheduledAt` ignores
  delay entirely; tier credit still subtracts.
- Integration (checkjobsvc): a due job with a huge stored `effective` (simulate
  a pre-migration row) is claimed immediately; ordering among due jobs follows
  `effective_scheduled_at`; migration 008 rewrites only rows where
  `effective > scheduled_at`.
- Worker: `delaySampleMs` measures vs `scheduled_at`; a probe that starts on
  time yields sample 0 (claimed-ahead jobs sleep until `scheduled_at`,
  `worker.go:547-568`, so spare capacity ⇒ 0).
- Manual (dev instance): after `make migrate`, the stranded browser jobs run
  again within one period; `GET /api/mgmt/scheduling/cost-distribution` delay
  percentiles decay toward true lateness. Treat any intermittent failure as a
  bug [[feedback_flaky_tests_are_bugs]].

## Key files

| File | Change |
|---|---|
| `server/internal/checkworker/scheduling/scheduling.go` | **~** cost-only offset, `MaxDeprioritizeOffset`, docs |
| `server/internal/checkworker/scheduling/scheduling_test.go` | **~** ordering/dead-band/clamp tests |
| `server/internal/checkworker/worker.go` | **~** `releaseLeaseWithCost`, `delaySampleMs` reference |
| `server/internal/checkworker/checkjobsvc/service.go` | **~** single gate, delete `effectiveWindowFactor` |
| `server/internal/checkworker/checkjobsvc/service_test.go` | **~** rework gate tests |
| `server/internal/db/{postgres,sqlite}/migrations/008_effective_reanchor.{up,down}.sql` | **+** heal polluted `effective_scheduled_at` |
| `server/internal/app/openapi/openapi.yaml` | **~** `delayEwmaMs` description |

## Risk log

| Risk | Mitigation |
|---|---|
| Removing delay from the offset changes claim order for existing fleets | Intended: victims stop being punished. Cost offsets are ≤ 60 s, well inside the window, so on-time behavior with spare capacity is unchanged. |
| Some future contributor re-adds an unbounded term to the offset | `MaxDeprioritizeOffset` clamp + invariant unit test. |
| Gate change misses an edge (job due but ordered last forever) | Anti-starvation now rests solely on the bounded offset: a job ≥ 60 s overdue sorts ahead of any on-time job. Covered by an explicit test. |
| Migration on stale/pre-consolidation dev DBs | Verify per [[project_migration_consolidation_stale_db]]; the UPDATE is idempotent. |

**Status**: Todo | **Created**: 2026-07-01 | **Extends**: `2026-06-30-09` | **Informed by**: `2026-07-01-01` Phase-3 measurement | **Blocks**: `2026-07-01-03`
