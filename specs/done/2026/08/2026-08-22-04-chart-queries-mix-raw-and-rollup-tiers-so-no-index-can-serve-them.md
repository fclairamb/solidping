---
model: opus
effort: high
---

# The check graph asks for `raw,hour` in one query, and the partial indexes on `results` cannot serve it

## Problem

`results` is the largest table in the system, and every index on it that
matters is **partial**, split on exactly one predicate — raw versus everything
else ([`001_v0_1_0.up.sql:455`](server/internal/db/postgres/migrations/001_v0_1_0.up.sql#L455)):

```sql
create index results_raw_idx        on results (organization_uid, check_uid, period_start desc)              where period_type  = 'raw';
create index results_aggregated_idx on results (organization_uid, check_uid, period_type, period_start desc) where period_type != 'raw';
```

[`chartFetchParams`](web/dash0/src/components/checks/response-time-chart.tsx#L168)
builds a **single** `periodType` list that straddles that split:

| range | `periodType` sent | index eligible? |
|---|---|---|
| hour | `raw` | ✅ `results_raw_idx` |
| day (period ≥ 5 min) | `raw` | ✅ `results_raw_idx` |
| day (period < 5 min) | `raw,hour` | ❌ **none** |
| week | `raw,hour` | ❌ **none** |
| month | `raw,hour,day` | ❌ **none** |

Postgres cannot prove that `period_type IN ('raw','hour')` satisfies either
partial predicate, so **neither index is eligible** and the only plan left is a
sequential scan of the whole table. This is structural, not a planner
misestimate: forcing `enable_seqscan = off` still produces a seq scan, at a
worse cost.

### Measured

A synthetic `results` table at production retention (24 h raw / 7 d hourly /
60 d daily), 30 orgs × 20 checks × 3 regions ≈ 3.0 M rows, 2.1 GB, with the
production indexes:

| query | plan | time |
|---|---|---|
| `periodType=raw`, 1 h window | Index Scan, 72 buffers | **3.3 ms** |
| `periodType=raw,hour`, 7 d window | Parallel Seq Scan, 239 k buffers (~1.9 GB) | **663 ms** cold / 404 ms warm |
| `periodType=raw,hour`, `enable_seqscan=off` | Parallel Seq Scan anyway | 5 540 ms |

The 1 h view is fast and every wider view is a full table scan — a **200×**
cliff produced entirely by the tier list. The scan cost is set by the size of
`results`, not by the window, so it grows with total history retained across
**all** orgs and gets worse forever.

### The boundary is raw-vs-rollup, and only that

Aggregated tiers combine freely — Postgres *can* prove
`period_type IN ('hour','day','month')` implies `period_type != 'raw'`:

| query | plan | time |
|---|---|---|
| `IN ('hour','day')`, 90 d | Bitmap Index Scan on `results_aggregated_idx` | 15.2 ms |
| `IN ('hour','day','month')`, 90 d | Index Scan on `results_aggregated_idx` | 2.2 ms |

So the fix is not "one query per tier". It is **never put `raw` in the same
query as a rollup tier** — at most two queries, whatever the range.

### The pattern already exists in this codebase

[`uptimebar.BucketAvailability`](server/internal/uptimebar/bucketing.go#L507)
solved this: two calls, `PeriodTypes: {hour, day}` and `PeriodTypes: {raw}`,
each time-bounded, each `SkipBlobs: true`, each with its own row cap. It
measures 15 ms on the same dataset. The chart path never adopted it.

### Two smaller defects on the same path

**`SkipBlobs` is never set for the chart.** `ListResultsFilter.SkipBlobs`
exists and drops `metrics` + `output` (by far the widest columns) from the
projection. Badges and status pages set it;
[`results.ListResults`](server/internal/handlers/results/service.go#L145)
never does. So every 1 000-row chart page decodes two jsonb blobs per row in
Go and discards them — `convertResultToResponse` filters by `with`, and the
chart's `with` list names no blob field. Invisible on the wire, real in
DB→app transfer and decode.

**The keyset cursor uses the OR form.** Both dialects emit
`(period_start < ?) OR (period_start = ? AND uid < ?)`
([postgres `:2398`](server/internal/db/postgres/postgres.go#L2400)), which
Postgres degrades to a BitmapOr plus a full sort instead of an index range
scan: 29 ms versus 3 ms on page 2 of a 24 h window, and the gap widens with
the window because the sort input grows while the page size does not.

## Proposal

**No schema change, and no new index.** A `(organization_uid, check_uid,
period_start desc, uid desc)` index would also fix the plan (663 ms → 99 ms
cold, measured) but costs 222 MB per 2 GB of table, has to be maintained on
the hottest insert path in the system, and is strictly worse than not issuing
the bad query. It is explicitly rejected.

### 1. Split the chart fetch at the raw boundary

`chartFetchParams` stops returning one `periodType` string and returns the
**tier plan** for the range — at most two entries, one rollup and one raw:

```ts
export interface ChartTierFetch {
  periodType: string;          // "hour,day" or "raw" — never mixed
  periodStartAfter: string;
  periodEndBefore?: string;
  with: string;
  size: number;
}
export function chartFetchParams(...): ChartTierFetch[]
```

The rollup entry keeps the full window. The raw entry keeps the full window
**for now** — narrowing it is spec `2026-08-22-07`, which builds on this one
and must not be folded in here.

The fetch layer issues the entries **concurrently** (`Promise.all`), not
serially, and merges the results into the single `ChartPoint[]` the rest of
the component already consumes. Ordering, gap detection and tier-transition
handling are unchanged — `detectGaps` already keys off `periodType` and
already treats a tier transition as "not a gap".

### 2. `useAllResults` gains a multi-tier sibling

The existing `useAllResults`
([`hooks.ts:1178`](web/dash0/src/api/hooks.ts#L1178)) stays as-is for its other
callers. Add `useResultTiers(org, tiers)` which runs one paginated walk per
tier in parallel and returns the merged array plus a single `isLoading`.

The react-query key must stay **derivable from the same inputs** as today, so
that the check-detail route's second `useAllResults` call
([`checks.$checkUid.index.tsx:615`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx#L615))
— which exists precisely to be a cache hit rather than a second HTTP request —
remains a cache hit. If that route now needs `useResultTiers` too, convert it;
what must not happen is the two diverging into two real fetches.

### 3. `SkipBlobs` when no blob field was requested

In `results.ListResults`, set `filter.SkipBlobs = true` unless `opts.With`
names a field backed by `metrics` or `output`. Derive it from the same
`with`-set the response conversion already builds, so the projection and the
serialization can never disagree — a blob asked for must never come back nil
because the projection dropped it.

### 4. Row-value keyset cursor

Replace the OR form with `(period_start, uid) < (?, ?)` in both dialects
([postgres `:2398`](server/internal/db/postgres/postgres.go#L2400),
[sqlite `:2280`](server/internal/db/sqlite/sqlite.go#L2336)). Both support
row-value comparison and both plan it as an index range scan. Semantics are
identical to the OR form; this is a rewrite, not a behaviour change.

### Testing

The green build proves nothing here — the current code is *correct*, just
slow, and every existing test passes against the seq scan. The tests must
prove the negative.

- **Plan assertions (Postgres, testcontainers).** Seed enough rows that a seq
  scan is distinguishable, then assert on `EXPLAIN` output that the chart's
  week and month queries use `results_raw_idx` / `results_aggregated_idx` and
  contain **no** `Seq Scan on results`. This is the test that would have caught
  the regression; a timing assertion would be flaky and must not be used
  instead.
- **A guard against re-mixing.** A unit test over `chartFetchParams` asserting
  that no returned entry's `periodType` contains both `raw` and a rollup tier,
  for every (range × periodMs) combination. This is the cheap check that keeps
  the fix from eroding.
- **Blob projection.** Assert that a `with` list naming no blob field returns
  rows with nil `metrics`/`output`, **and** that a `with` list naming one still
  returns it populated — the positive control, without which the first
  assertion passes trivially if `SkipBlobs` were set unconditionally.
- **Cursor parity.** Paginating a window with the row-value cursor returns the
  same UID sequence, in the same order, with the same page boundaries, as the
  OR form did.
- **Merged-series equivalence.** For a seeded check, the `ChartPoint[]` built
  by the split fetch is identical to what the single mixed query produced —
  same points, same order, same gap regions.

### Acceptance

- No chart range issues a query whose `periodType` mixes `raw` with a rollup
  tier, at any check period, zoomed or not.
- `EXPLAIN` for every chart range shows an index scan on `results`.
- The check-detail route still makes **one** HTTP request per tier, not two —
  the region/stats derivation stays a react-query cache hit.
- Chart rendering, gap regions, zoom, region filtering and pinned-result
  selection are visually and behaviourally unchanged.

## Out of scope

- Progressive/two-pass rendering and narrowing the raw window — spec
  `2026-08-22-07`, which depends on this one.
- The status-page response-time query — spec `2026-08-22-05`.
- Recharts render cost. The render path already sets `isAnimationActive={false}`
  and gates dots by density; `dot`/`activeDot` being inline arrows recreated
  each render is a real but separate issue, and no React profiling has been
  done. Do not speculatively rework it here.

## Implementation Plan

1. **`chartFetchParams` returns a tier plan, never a mixed list.**
   `web/dash0/src/components/checks/response-time-chart.tsx`: replace
   `ChartFetchParams` with `ChartTierFetch` and return `ChartTierFetch[]` — at
   most two entries, the rollup entry first (`"hour"` / `"hour,day"`) and the
   raw entry second (`"raw"`), never one string containing both. Both entries
   keep the full window (narrowing raw is spec `2026-08-22-07`). Zoom handling
   is unchanged: the window comes from the zoom, the tier from the zoom span.

2. **`useResultTiers` in `web/dash0/src/api/hooks.ts`.**
   Extract the existing cursor-walk body of `useAllResults` into
   `fetchAllResultPages` and leave `useAllResults` behaviourally identical.
   Add `useResultTiers(org, checkUid, tiers, opts)` built on `useQueries`, one
   query per tier, each with the **same react-query key shape** as
   `useAllResults` (`["allResults", org, {checkUid, periodType, …}]`) so the
   check-detail route's parallel call is a cache hit rather than a second HTTP
   request. Tiers run concurrently; the hook returns the merged array plus a
   single `isLoading`.

3. **Merge preserves the single-query ordering.**
   Exported `mergeResultTiers` sorts the concatenated tiers by
   `period_start DESC, uid DESC` — byte-identical to what the server's
   `ORDER BY result.period_start DESC, result.uid DESC` produced for the mixed
   query, so the chart's `[...data].reverse()`, `detectGaps` and pinned-result
   handling need no change.

4. **Convert the check-detail route.**
   `routes/orgs/$org/checks.$checkUid.index.tsx` switches its second
   `useAllResults` to `useResultTiers` with the same `chartFetchParams(...)`
   output, keeping the cache-hit invariant.

5. **`SkipBlobs` when no blob field was requested.**
   `server/internal/handlers/results/service.go`: derive
   `filter.SkipBlobs = !needsBlobs(opts.With)` from the same lowercased
   `with`-set `convertResultToResponse` builds, so the projection and the
   serialization can never disagree.

6. **Row-value keyset cursor in both dialects.**
   `applyResultsFilter` in `server/internal/db/postgres/postgres.go` and
   `server/internal/db/sqlite/sqlite.go`: replace
   `(period_start < ?) OR (period_start = ? AND uid < ?)` with
   `(period_start, uid) < (?, ?)`.

### Tests

- `server/internal/db/postgres/chart_results_plan_postgres_test.go` — EXPLAINs
  the **production** statement (built via `applyResultsFilter`, as
  `uptimebar_plan_postgres_test.go` does) for every tier list
  `chartFetchParams` can emit (`hour`, `hour,day`, `raw`), asserting an index
  scan on `results_raw_idx` / `results_aggregated_idx` and **no**
  `Seq Scan on results`; positive controls run the pre-fix mixed predicates
  (`raw,hour` and `raw,hour,day`) on the same fixture and require that they DO
  seq-scan. A second pair covers the cursor: the row-value form must stay an
  index scan with the cursor columns in the Index Cond, while the old OR form
  on the same data is the control.
- `server/internal/db/sqlite/chart_results_plan_test.go` — `EXPLAIN QUERY PLAN`
  over the same production builder: the tier queries and the cursor page must
  use `results_raw_idx` / `results_aggregated_idx` with no `SCAN results`.
- `server/internal/db/sqlite/results_cursor_parity_test.go` — paginating a
  window with the row-value cursor yields the identical UID sequence and page
  boundaries as the OR form (run side by side over the same seed, including
  rows sharing a `period_start` so the tie-break is exercised).
- `server/internal/handlers/results/service_blob_projection_test.go` — a `with`
  list naming no blob field returns nil `metrics`/`output`; a `with` list
  naming `metrics` (and one naming `output`) still returns them populated —
  the positive control that fails if `SkipBlobs` were set unconditionally.
- `web/dash0/src/components/checks/response-time-chart.test.ts` — for every
  (range × periodMs × zoom-span) combination, no returned entry's `periodType`
  mixes `raw` with a rollup tier, at most one raw entry and at most one rollup
  entry are returned, and the union of the returned tiers equals the tier set
  the old mixed query used (so the split loses no data).
- `web/dash0/src/api/result-tiers.test.ts` — `mergeResultTiers` reproduces the
  exact ordering of the single mixed query for a seeded two-tier dataset,
  including `period_start` ties broken by uid.
