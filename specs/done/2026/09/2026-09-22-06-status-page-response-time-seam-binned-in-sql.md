---
model: opus
effort: high
---

# The status page fetches ~1,300 raw probes per check to plot ~30 seam points; bin the raw seam in SQL

## Problem

`fetchRecentResults`
([`service.go`](server/internal/handlers/statuspages/service.go), search
`func (s *Service) fetchRecentResults`) feeds the per-resource response-time
chart. Since spec `2026-09-21-03` it fetches, per check, a raw tier and a
rollup tier through the `RecentResultsPerCheck` LATERAL statement, then
`trimWindowedResponseTimeRows` splits the rows into raw / hour / day, gives
each tier a share of the 100-point budget, and `subsampleRows` keeps every
n-th row of each tier.

The raw tier is sized by `responseTimePerRegionBudget`: for a 1-minute check
on a 7-day page the "raw appetite" is `ceil(26 h × 60) = 1,560` rows per
region, times `min(regions+1, 20)` = 3,120 per check. Measured on the dev
deployment (200 checks, one region, 7-day window), `EXPLAIN (ANALYZE, BUFFERS)`
on the exact statement:

| | Today | Raw seam pre-binned per hour in SQL |
|---|---|---|
| Rows returned | 292,843 (≈ 1,337 raw + 127 rollup per check) | 4,665 |
| Database time | 834 ms | 729 ms |
| Wall time inside the request (slow-query log) | **2.4–3.0 s** | ≈ 0.8 s |

Then Go throws 99 % of it away: the raw share of the budget for a 7-day
window is `26 h / 168 h × 100 ≈ 15` points, so `subsampleRows` keeps roughly
**one probe in ninety** and plots those as if they were representative. A
probe picked every 90 minutes is not a response-time series; it is noise with
a nice x-axis. The hour rollups next to it carry a real `duration_p95` over
sixty probes. The seam should carry the same kind of number.

## Decision

The raw seam is **binned in the database**: one row per `(check, region, bin)`
with a probe count, an up count, p95 / avg / min / max duration and the status
mix, computed over every probe in the bin. The rollup tier keeps the LATERAL
fetch it has today. The trim and budget logic downstream is unchanged except
that the seam tier now arrives already reduced, so its budget is a formality.

This is both the performance fix (293k rows → a few thousand) and a
correctness fix: a seam point becomes a p95 over its bin, like every rollup
point, instead of one arbitrary probe.

## Design

### Bin width

```go
// seamBinWidth picks the raw seam's bin so the seam alone never exceeds the
// point budget, never goes below the probe period floor, and never gets
// coarser than the hour tier it sits next to.
func seamBinWidth(windowSpan time.Duration) time.Duration {
    ideal := windowSpan / responseTimeLimit                 // 100 points across the window
    steps := []time.Duration{1*Minute, 2*Minute, 5*Minute, 10*Minute, 15*Minute, 30*Minute, time.Hour}
    // smallest step >= ideal, clamped to [1 min, 1 h]
}
```

24 h page → 15 min bins (≈ 96 seam points). 7-day page → 1 h. 30 and 90 days →
1 h, so the seam is never coarser than the hour rollups it joins. Bins are
aligned like Go's `Truncate` (see spec `2026-09-22-05` for the origin rule;
reuse its bucket expression helper in both dialects).

### New DB method

```go
// AggregateResponseTimeBins bins raw results per (check, region, bin) for the
// status page's response-time seam. Rows with a status excluded from
// availability (created/running/abandoned) are dropped before binning; a bin
// with no duration at all still comes back (counts only) so the availability
// colouring of the point stays honest.
AggregateResponseTimeBins(ctx context.Context, f *models.ResponseTimeBinFilter) ([]models.ResponseTimeBin, error)
```

```go
type ResponseTimeBinFilter struct {
    OrganizationUID string
    CheckUIDs       []string
    Since           time.Time      // uptimebar.RawTierStart(...), the raw clamp
    BinDuration     time.Duration
}

type ResponseTimeBin struct {
    CheckUID  string
    Region    *string           // NULL region stays NULL, as on raw rows
    BinStart  time.Time
    Total, Up int
    DurationP95, DurationAvg, DurationMin, DurationMax *float32 // nil when no probe carried a duration
    StatusCounts map[int]int    // status → probes, for the dominant-status rule
}
```

One statement for all checks (`check_uid IN (?)`, no LATERAL: the output is
bounded by `checks × regions × bins`), on `results_raw_idx` with the restated
`period_type = 'raw'` predicate.

**p95.** Match the definition the aggregation job uses for `duration_p95`
(read `job_aggregation.go`'s percentile helper first; it is the number the hour
tier already shows, and the seam must not disagree with the hour that will
replace it). If the job uses nearest-rank, implement nearest-rank in SQL with a
window function (`ROW_NUMBER() OVER (PARTITION BY check_uid, region, bin
ORDER BY duration)` against `COUNT(*) OVER (…)`), which is portable to both
dialects; Postgres' `percentile_cont` interpolates and is not what the job
does. Only probes with a non-NULL duration take part in the percentile.

**Status mix.** Return the per-status counts — `jsonb_object_agg(status,
n)` in Postgres, `json_group_object` in SQLite, over a per-status sub-aggregate
— and derive the point's status in Go with the aggregation job's own
`calculateDominantStatus` (majority, severity tie-break, warning → degraded).
Move that function to a shared home (`uptimebar.DominantStatus`) and make the
job call the shared copy, pinned by a test that its results are unchanged. The
seam then classifies exactly like an hour rollup would.

### In-memory seam rows

`trimWindowedResponseTimeRows` and `buildResponseTimeData` work on
`[]*models.Result`. Materialise each bin as a `models.Result` with
`PeriodType = models.PeriodTypeSeam` (new constant, **never persisted** —
document it next to `PeriodTypeRaw`… and add a model test that no writer
accepts it), `PeriodStart = BinStart`, `TotalChecks`, `SuccessfulChecks`,
`DurationP95/Avg/Min/Max`, `Status = dominant`, `Region`.

- `uptimebar.StatsForResult`: `seam` folds like a rollup (`accumulateAgg`).
- `trimWindowedResponseTimeRows`: `seam` is budgeted as the raw tier
  (`rawRows`), so `responseTimeTierBudgets` keeps its meaning.
- `buildResponseTimeData` already prefers `DurationP95` over `Duration`, so a
  seam point renders with no change. The `time` of the point is the bin start,
  like a rollup's `period_start`.
- `responseTimePointsHaveSignal` is unchanged; a seam with only durationless
  probes has `DurationP95 == nil` and drops out as before.

### What is deleted

- The raw tier of `RecentResultsPerCheck` as called from `fetchRecentResults`
  (the DB method keeps supporting a raw tier for its other callers, if any;
  check `dash0`'s seam fetch which spec `2026-09-21-03` mentions).
- `rawAppetite`, `responseTimeRawFetchCap`, `rawSeamMargin` as inputs to the
  per-check budget; `responseTimePerRegionBudget` keeps only the rollup
  appetite.
- `GetChecksByUIDs` in `responseTimeBudgets` stays only if the rollup budget
  still needs the region count; otherwise drop it (one query fewer).

### Visible change, stated for the docs

Seam points on the response-time chart were individual probes sub-sampled at
up to one in ninety. They are now the p95 over a bin (15 min on a 24 h page,
1 h otherwise) with the probe counts the tooltip already shows for rollup
points. The "current" point is the open bin and moves as probes land.

## Tests

1. **Parity, both dialects**: seed raw rows with NULL durations, excluded
   statuses, two regions and a NULL region; assert the bins' counts, p95 (against
   the job's helper over the same rows), avg/min/max and status counts. Include a
   bin whose probes all lack a duration.
2. `seamBinWidth` table test (24 h → 15 min, 7 d → 1 h, 90 d → 1 h, 1 h → 1 min).
3. `DominantStatus` relocation: the job's existing tests pass unchanged against
   the shared function.
4. `statuspages` service tests for `buildAvailabilityData` /
   `trimWindowedResponseTimeRows` with seam rows: budgets, ordering, the marker
   phantom (spec `2026-09-21-03` A.4) still dropped, a seam-only region still
   rendered.
5. Plan test (Postgres): the bin statement uses `results_raw_idx` and has no
   external sort.
6. status0: `response-time-chart` unit tests unchanged (the payload shape is
   identical); one e2e that a page with a 24 h period shows seam points 15 min
   apart on the numeric axis.

## Docs

- `web/docs/docs/features/status-pages.md`: the response-time chart paragraph
  states what a point is (p95 over the bin / hour / day).
- `wiki/features/results-aggregation.md`: the seam is binned on read; the
  `seam` period type is in-memory only.
- Changelog entry.

## Acceptance

- The LATERAL statement disappears from the dev deployment's slow-query log
  for the 200-check page; the page view drops by ≥ 1.5 s on top of spec
  `2026-09-22-05`.
- `make test-postgres` green.

## Out of scope

- Reworking the point budget split across tiers.
- dash0's own seam fetch for the check detail chart (same idea, different
  consumer, own spec).

---

## Implementation Plan

Sequenced so each step lands green on its own.

1. **`models.PeriodTypeSeam` + the never-persisted guard.** Add the constant next
   to `PeriodTypeRaw` with the in-memory-only contract stated there, plus
   `ErrPeriodTypeNotPersistable` and a `bun.BeforeAppendModelHook` on
   `models.Result` that refuses a seam row on any INSERT/UPDATE. The hook is the
   enforcement point rather than a check at each of the five writer methods per
   dialect: it covers `CreateResult`, `CreateResults`,
   `SaveResultWithStatusTracking`, `UpsertAggregatedResult`/`CompactResults` and
   anything added later, from one place.

2. **`uptimebar.DominantStatus`.** Move `calculateDominantStatus` out of
   `jobs/jobtypes/job_aggregation.go` verbatim (comment included) and have the
   job call the shared function. The job's existing table test keeps its body
   unchanged, bound to the shared function by a one-line alias in the test
   package — that is the "results unchanged" pin.

3. **`models.ResponseTimeBinFilter` / `ResponseTimeBin`** in a new
   `models/response_time_bin.go`, with `Validate()` in the shape
   `ResultBucketFilter.Validate` has.

4. **`AggregateResponseTimeBins`, both dialects.** One statement, built by a
   `responseTimeBinsSQL(filter) (string, []any)` helper per dialect so the plan
   test EXPLAINs the production text (the `recentResultsPerCheckSQL` pattern).
   Shape: a `probes` CTE (org + `check_uid IN (?)` + `period_type IN ('raw')` +
   restated `period_type = 'raw'` + `period_start >= ?` + `status NOT IN
   (excluded)`), reusing spec 05's `resultBucketExpr` for the bin, then three
   CTEs joined on `(check_uid, region, bin)` with NULL-safe equality
   (`IS NOT DISTINCT FROM` / SQLite `IS`, so the NULL region stays NULL rather
   than being coalesced into a group that a literal `''` region could collide
   with):
   - `agg` — total, up, dur_cnt, avg/min/max;
   - `rank` — nearest-rank p95 via `ROW_NUMBER()`/`COUNT(*) OVER`, picking
     `rn = (cnt*19)/20 + 1`. That integer form is exactly the job's
     `int(float64(n)*0.95)` index (0-based) for every n — verified, and pinned by
     a Go test — and avoids both float drift and Postgres numeric-literal
     arithmetic. Only non-NULL durations enter it;
   - `mix` — `jsonb_object_agg` / `json_group_object` over a per-status
     sub-aggregate.
   `agg` is the driving side with LEFT JOINs, so a bin whose probes all lack a
   duration still comes back with counts and NULL durations.
   Register it on the `db.Service` interface.

5. **`seamBinWidth`** in `statuspages/service.go`: the step table, smallest step
   >= `windowSpan / responseTimeLimit`, clamped to [1 min, 1 h].

6. **Seam rows in `fetchRecentResults`.** Drop the raw tier from the
   `RecentResultsPerCheck` filter (the DB method keeps supporting one), call
   `AggregateResponseTimeBins` with `uptimebar.RawTierStart(...)` and
   `seamBinWidth(windowSpan)`, and materialise each bin as a `*models.Result`
   with `PeriodType = PeriodTypeSeam`. Downstream:
   - `sortResponseTimeRows` — seam is a FINER tier than any rollup, same rung as
     raw;
   - `trimWindowedResponseTimeRows` — seam joins `rawRows`;
   - `StatsForResult` already routes a non-`raw` period type to `accumulateAgg`,
     which is the rollup fold the spec asks for — no change needed there, only a
     comment naming seam;
   - `buildResponseTimeData` / `responseTimePointsHaveSignal` unchanged.
   An UNBOUNDED fetch (zero `windowStart`, the legacy/parity path) keeps the raw
   tier: there is no window to size a bin from, and the parity tests pin that
   map shape.

7. **Deletions.** `rawAppetite`, `responseTimeRawFetchCap`, `rawSeamMargin`; the
   `checkPeriod`/`retentionRawHours` parameters of
   `responseTimePerRegionBudget` and the now-unused `hints` parameter of
   `responseTimeBudgets`. `GetChecksByUIDs` STAYS — the rollup budget still
   multiplies by the check's own region count.

8. **Tests**, in the order of the spec's Tests section: dialect parity
   (`response_time_bins_postgres_test.go` / `response_time_bins_test.go`, folding
   the same fixture through the job's own `calculateRawMetrics`),
   `seamBinWidth` table test, the `DominantStatus` alias pin, statuspages trim
   tests with seam rows, the Postgres plan test, and the status0 e2e.

9. **Docs**: `web/docs/docs/features/status-pages.md`,
   `wiki/features/results-aggregation.md`, `CHANGELOG.md`.
