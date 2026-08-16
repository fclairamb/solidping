---
model: opus
effort: high
---

# Uptime-bar availability queries sequentially scan the whole `results` table

## Problem

Every status-page render and every availability widget runs a query that the
Postgres planner can only satisfy with a **parallel sequential scan of the
entire `results` table**. Measured on `solidping.k8xp.com` (2026-08-16), the
status page took **2.2–6.8 s** end to end; port-forwarding straight to the pod
reproduced it identically (4.1–5.3 s), so this is the query, not the ingress.

The captured plan, for a 30-day window over 5 checks:

```
Limit  (actual rows=17424)
  ->  Gather Merge  (Workers Launched: 2)
        ->  Sort  (Sort Key: period_start DESC, uid DESC)
              ->  Parallel Seq Scan on results
                    Rows Removed by Filter: 296993      (x3 workers ~ 891k)
                    Buffers: shared hit=31266 read=9308  (~318 MB)
Execution Time: 527 ms (warm)  /  2377 ms (cold)
```

It reads ~318 MB and discards ~891k rows to return 17,424.

### Why the planner cannot use an index

`results` has exactly two useful indexes, and **both are partial**:

```sql
results_raw_idx         ... (organization_uid, check_uid, period_start DESC)
                            WHERE (period_type = 'raw')
results_aggregated_idx  ... (organization_uid, check_uid, period_type, period_start DESC)
                            WHERE (period_type <> 'raw')
```

Both call sites ask for raw **and** rollup tiers in a single `IN` list:

- [bucketing.go:257](server/internal/uptimebar/bucketing.go:257) —
  `PeriodTypes: []string{PeriodTypeRaw, PeriodTypeHour, PeriodTypeDay}`
- [window.go:61](server/internal/uptimebar/window.go:61) —
  `PeriodTypes: []string{PeriodTypeRaw, PeriodTypeHour, PeriodTypeDay, PeriodTypeMonth}`

which the DB layer renders as `period_type IN (?)` —
[postgres.go:2203](server/internal/db/postgres/postgres.go:2203),
[sqlite.go:2150](server/internal/db/sqlite/sqlite.go:2150).

`period_type IN ('raw','hour','day')` is implied by *neither* partial predicate,
so neither index is usable and the planner falls back to a seq scan. This was
verified directly against the dev database — the same query split by tier uses
an index in both directions:

| Query | Plan | Time (warm) |
|---|---|---|
| `period_type IN ('raw','hour','day')` — **current** | Parallel Seq Scan | **661 ms** |
| `period_type IN ('hour','day')` | Index Scan `results_aggregated_idx` | **9.8 ms** |
| `period_type = 'raw'`, bounded to last 24 h | Bitmap Index Scan `results_raw_idx` | **97 ms** |
| `period_type = 'raw'`, bounded to last 48 h | Bitmap Index Scan `results_raw_idx` | 622 ms |

Postgres *does* prove `IN ('hour','day')` implies `period_type <> 'raw'`, so the
non-raw tiers need no new index — splitting the query is sufficient.

### Why it turned slow suddenly rather than gradually

The instance writes **831,353 raw rows per 24 h**, and raw retention is 24 h
([job_aggregation.go:312](server/internal/jobs/jobtypes/job_aggregation.go:312)),
so the steady-state heap is ~317 MB. The cluster's `shared_buffers` is **256 MB**.
The scan's working set recently outgrew cache, so it went from memory-speed to
disk-speed — a cliff, not a slope. The plan above shows the real disk reads
(`read=9308`). Aggregation and retention are **healthy** and were ruled out:
only 552 raw rows cluster-wide are past their rollup boundary, and the newest
`hour` rollup sits exactly on the 24 h line as designed.

This means the problem is **not** specific to one large org — it scales with
total ingest, so it will recur and worsen on any deployment as check volume grows.

## Proposal

Split the single multi-tier query into **two tier-aligned queries** in both
`BucketAvailability` and `WindowAvailability`, and merge the rows before
bucketing. Do not add an index — the existing partial indexes already cover
both halves once the predicate stops straddling them.

### 1. Rollup tiers — full window

Query `hour`/`day`(/`month`) over the caller's full `[start, end)` window,
unchanged apart from dropping `raw` from `PeriodTypes`. This alone moves the
dominant cost from 661 ms to ~10 ms.

### 2. Raw tier — bounded to the retention window

Query `period_type = 'raw'` separately, with `PeriodStartAfter` clamped to

```
max(windowStart, now - (retentionRaw + margin))
```

Raw older than `retentionRaw` does not exist: the aggregation job compacts a
bucket and deletes its source rows **in one transaction**
([job_aggregation.go:~200](server/internal/jobs/jobtypes/job_aggregation.go:200)),
so raw and rollups are disjoint by construction and the clamp cannot drop data
that a rollup does not already cover. Take `retentionRaw` from the org config the
caller already threads through (`retentionRawHours`, defaulting to
`defaultRetentionRawHours` = 24).

Use a **small** margin (suggest 2 h, not 24 h) to absorb aggregation lag: the
measurements above show the raw tier is the whole remaining cost and scales
sharply with the bound — 24 h costs 97 ms, 48 h costs 622 ms. If any raw row
comes back at the oldest edge of the clamp, `slog.WarnContext` that aggregation
is lagging, matching the existing "generous cap + log + return partial" pattern
in `BucketAvailability`.

### 3. Correctness constraints

- **Never double-count.** The accumulator branches on `PeriodType`
  ([bucketing.go:~293](server/internal/uptimebar/bucketing.go:293)) and adds raw
  and rollup rows into the *same* `BucketStats`. Disjointness is what keeps that
  correct today; the clamp preserves it, but any change that widens the raw
  bound past a rollup boundary would silently inflate totals. Add a test that
  fails if a bucket receives both a raw row and a rollup for the same period.
- **Keep the empty/no-data semantics.** A check absent from the map, or with
  `Total == 0`, must still render as "no data", never "100 %" —
  see the contract documented at [window.go:~40](server/internal/uptimebar/window.go:40).
- **Preserve `SkipBlobs`** on both queries (spec 2026-07-24-02 §5).
- **Both backends.** The split lives in `uptimebar`, so Postgres and SQLite both
  inherit it; verify SQLite still returns identical buckets
  (`make sync-pg-to-sqlite` conventions apply).

### 4. Re-derive `safetyRowCap`

`safetyRowCap` currently produced **`LIMIT 884300`** on the observed request —
large enough to be functionally unbounded, so it never protected anything while
also denying the planner any early-termination shape. Once the tiers are split,
size each cap to its own tier (raw: retention window × probe rate × regions;
rollups: one row per bucket per tier) so the cap is a real guard rather than a
formality. Keep the existing warn-and-return-partial behaviour when it engages.

## Verification

- `EXPLAIN (ANALYZE, BUFFERS)` on both new queries must show `Index Scan` or
  `Bitmap Index Scan` — **no `Seq Scan` on `results`**. Add this as an assertion
  in a Postgres integration test (testcontainers), seeded with enough rows that
  a seq scan would be chosen if the predicate regressed. This is the test that
  actually pins the fix; a timing assertion would be flaky.
- Status-page p95 for a 30-day, 5-check window should land in the tens of ms
  server-side, against ~530–2400 ms today.
- Bucket values must be byte-identical before and after for a fixed dataset —
  this is a pure query-shape change, not a semantics change.
