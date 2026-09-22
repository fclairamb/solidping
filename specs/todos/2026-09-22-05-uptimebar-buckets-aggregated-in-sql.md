---
model: opus
effort: high
---

# `uptimebar` streams every raw row of the last 24 h into Go to compute a handful of daily buckets

## Problem

[`uptimebar.BucketAvailabilityInRegions`](server/internal/uptimebar/bucketing.go)
is the one helper behind every availability number in the product: the status
page's bars and its page-level percentage, the summary endpoint, the badges, the
SLO read path, the availability API and the uptime report all call it. It
answers "per check, per bucket: how many probes, how many up, maintenance
share, duration sum/min/max, slow samples" — a few integers per
`(check, bucket)`.

It computes that by fetching **every matching result row** through
`db.ListResults` (two statements, one per index side: raw, then hour+day) and
folding them in Go (`accumulateRaw` / `accumulateAgg`). Rollups are cheap. Raw
is not: the aggregation job only writes an hour rollup once the hour has aged
past the raw retention (`calculateAggregationBoundary`: `truncate(now, hour) −
(rawHours−1)h`), so under the default 24 h retention **the newest 24 h of every
check is always raw**, and every caller re-reads all of it on every request.

Measured on the dev deployment, one public page, 200 checks at a 1-minute
period, 7-day history (the server's own slow-query log, then `EXPLAIN (ANALYZE,
BUFFERS)` on the identical statement against the same database):

| | As run today | Same data, aggregated in SQL |
|---|---|---|
| Rows shipped to Go | 267,449 | 400 (200 checks × 2 days touched) |
| Plan | Parallel Seq Scan + Sort, **external merge on disk (12.8 MB)** | Bitmap Index Scan on `results_raw_idx` + HashAggregate |
| Database time | 990 ms | 520 ms |
| Wall time inside the request | **3.1–3.4 s** | ~0.5 s |

The 2+ s gap between database time and wall time is the transfer and the bun
scan of 267k rows × 20 columns into `[]*models.Result`, for a fold that keeps
eleven numbers per bucket. The rollup tier is 25k rows in 125 ms and is fine.

Two side effects of the row path go away with it:

- `applyResultsFilter` orders every statement by `(period_start DESC, uid
  DESC)`; this consumer accumulates into a map and never needed the order. The
  planner honours it anyway — that is the on-disk sort above.
- `rawRowCap` / `rollupRowCap` / `MeasureRawRowsPerHour` exist only to bound
  the number of rows the fold has to hold. Sizing them costs an extra query per
  request (`ListOrgCheckRates`) and they emit a "hit its safety row cap;
  returning partial data" warning that is a **silent wrong answer** on a large
  page. With an aggregate the output is bounded by `checks × buckets` by
  construction.

## Decision

Replace the two `ListResults` fetches in `BucketAvailabilityInRegions` with two
`GROUP BY` statements (one per index side, as today) that return one row per
`(check_uid, bucket_start)`, and fold those. Every consumer keeps the exact
same `map[checkUID]map[bucketStart]BucketStats` and the exact same numbers.

`BucketStats` is unchanged. The SQL computes each of its fields the way the Go
accumulators do; the accumulators stay in the package as the **reference
implementation** the parity test runs against.

## Design

### New DB method

`db.Service` gains one method, implemented for both dialects:

```go
// AggregateResultBuckets folds result rows into per-(check, bucket) counters
// server-side. One tier side per call (raw XOR rollup), exactly like
// ListResults' index split (models.PeriodTypesTierSide).
AggregateResultBuckets(ctx context.Context, f *models.ResultBucketFilter) ([]models.ResultBucket, error)
```

```go
type ResultBucketFilter struct {
    OrganizationUID  string
    CheckUIDs        []string
    Regions          []string      // optional WHERE, never a GROUP BY key
    PeriodTypes      []string      // one tier side; Validate rejects mixed
    PeriodStartAfter time.Time
    BucketDuration   time.Duration // > 0
}

type ResultBucket struct {
    CheckUID    string
    BucketStart time.Time
    Total, Up, MaintTotal, MaintUp int
    DurCnt      int
    DurSum      float64
    DurMin, DurMax *float32   // nil when DurExtremaCnt == 0
    DurExtremaCnt int32
    SlowSamples, SlowPeaks int32
    Rows        int           // source rows folded (raw lag warning, diagnostics)
    OldestPeriodStart time.Time
}
```

### Bucket expression: reproduce Go's `Truncate` exactly

The Go fold keys on `PeriodStart.UTC().Truncate(bucketDuration)`, and
`time.Truncate` rounds down **relative to the zero `time.Time`** (0001-01-01
00:00:00 UTC), not the Unix epoch. The availability API accepts any whole
multiple of its `minBucket` (`resolveBucket`), so widths that do not divide
24 h (7 h, 90 min…) are legal and epoch-aligned binning would put rows in
different buckets than today. Use the same origin as Go:

- Postgres (18 on the reference deployment; `date_bin` needs 14+):
  `date_bin(make_interval(secs => ?), period_start, TIMESTAMPTZ '0001-01-01 00:00:00+00')`.
  Postgres uses the proleptic Gregorian calendar, as Go does, so the two grids
  coincide for every width.
- SQLite (`period_start` is ISO text): compute in epoch seconds with the same
  origin shifted, `((unixepoch(period_start) + 62135596800) / ?) * ? - 62135596800`,
  and convert back in Go. `unixepoch()` needs SQLite ≥ 3.38; both bundled
  drivers (`modernc.org/sqlite v1.59`, `mattn/go-sqlite3 v1.14.52`) ship newer
  engines. `strftime('%s', …)` is the fallback if a test environment proves
  older.

Add a unit test that feeds a few hundred random timestamps and every width the
callers use (1 h, 24 h, and a non-divisor such as 7 h) through both the SQL
expression and `time.Truncate` and asserts identical bucket starts.

### Raw tier

Mirror `accumulateRaw` field by field. Rows with `status IN (1, 2, 9)`
(created, running, abandoned — `ResultStatus.ExcludedFromAvailability`) are
excluded before aggregation; the constants come from `models`, not literals in
the SQL string.

```sql
SELECT check_uid, <bucket_expr> AS bucket_start,
       COUNT(*)                                            AS total,
       COUNT(*) FILTER (WHERE status IN (3, 8))            AS up,          -- CountsAsUp
       COUNT(*) FILTER (WHERE maintenance)                 AS maint_total,
       COUNT(*) FILTER (WHERE maintenance AND status IN (3, 8)) AS maint_up,
       COUNT(duration)                                     AS dur_cnt,
       COALESCE(SUM(duration), 0)                          AS dur_sum,
       MIN(duration), MAX(duration),
       COUNT(duration)                                     AS dur_extrema_cnt,
       COUNT(*) FILTER (WHERE duration > 1000)             AS slow_samples, -- SlowSampleThresholdMillis
       0                                                   AS slow_peaks,
       COUNT(*) AS rows, MIN(period_start) AS oldest_period_start
FROM results
WHERE organization_uid = ? AND check_uid IN (?) [AND region IN (?)]
  AND period_type IN ('raw') AND period_type = 'raw'      -- restated for the partial index, as applyPeriodTypeFilter does
  AND period_start >= ? AND status NOT IN (1, 2, 9)
GROUP BY 1, 2
```

SQLite has no `FILTER`; use `SUM(CASE WHEN … THEN 1 ELSE 0 END)`. Read
`foldExtrema` before writing `dur_extrema_cnt`; the SQL must count what the Go
fold counts.

### Rollup tier

Mirror `accumulateAgg`: `SUM(total_checks)`, `SUM(successful_checks)`,
`SUM(maintenance_checks)`, `SUM(maintenance_successful_checks)` (SQL `SUM`
ignores NULL, which is exactly the "nil contributes nothing" rule),
`dur_sum = SUM(duration_avg * total_checks) FILTER (WHERE duration_avg IS NOT
NULL AND total_checks > 0)`, `dur_cnt = SUM(total_checks) FILTER (same)`,
extrema from `duration_min` / `duration_max` with the one-sided fold
(`COALESCE(duration_min, duration_max)` / `COALESCE(duration_max,
duration_min)`), `slow_peaks = COUNT(*) FILTER (WHERE duration_max > 1000)`,
`slow_samples = 0`. Predicate `period_type IN (?) AND period_type != 'raw'`.

### `uptimebar` changes

- `ResultsLister` becomes `ResultBucketAggregator` with the new method; every
  `db.Service` already satisfies it. The test fakes in `bucketing_test.go`,
  `latency_test.go`, `maintenance_test.go`, `region_test.go` are rewritten to
  serve buckets — see Tests for how they stay honest.
- `BucketAvailabilityInRegions` calls the aggregate twice (raw with the
  `rawTierStart` clamp, rollup with the window start), folds `ResultBucket`
  into `BucketStats` with a trivial `add`, and keys on `BucketStart`.
- `warnIfRawLagging` reads `MIN(OldestPeriodStart)` over the raw buckets
  instead of scanning rows.
- Delete `rawRowCap`, `rollupRowCap`, `listTier`, `capMaxRegionsPerCheck`,
  `capSafetyMargin`, `capRateHeadroom`, `defaultRetentionHourDays` (if it only
  served the cap), `Hints.RawRowsPerHour`, `MeasureRawRowsPerHour`,
  `CheckRateLister`, and the `uptimebarHints` call to `MeasureRawRowsPerHour`
  in `statuspages`. `Hints` keeps the two retention values. If
  `ListOrgCheckRates` has no other caller, delete it too.
- `RawTierStart` / `rawTierStart` stay: the raw clamp is what keeps the raw
  statement on `results_raw_idx` and is reused by spec `2026-09-22-06`.

### Consumers

None change at the call site: `statuspages` (daily and hourly paths),
`badges`, `slos`, `uptimereport/daystrip.go`, `availability/buckets.go`. Their
tests keep passing unmodified; that is the point.

## Tests

1. **Parity, both dialects** (`internal/db/postgres` and `internal/db/sqlite`,
   the Postgres one under `SP_TEST_REQUIRE_POSTGRES`): seed a few checks with a
   deliberately nasty mix — raw rows across `created`/`running`/`up`/`warning`
   /`down`/`timeout`/`error`/`abandoned`, some with NULL duration, some in
   maintenance, two regions, plus hour and day rollups with NULL maintenance
   counters and one-sided extrema. Fold the same rows with the Go accumulators
   and assert the aggregate returns identical `BucketStats` for every bucket, at
   1 h and 24 h, with and without a `Regions` filter.
2. **Bucket-origin test** described above.
3. **Tier validation**: a mixed `PeriodTypes` is rejected, like
   `RecentResultsPerCheckFilter.Validate`.
4. **Plan test** (Postgres, like the one for `recentResultsPerCheckSQL`):
   `EXPLAIN` of the raw statement uses `results_raw_idx`; of the rollup
   statement, `results_aggregated_idx`; neither plan contains a `Sort` node.
5. The existing `uptimebar` tests: keep the scenarios, swap the fake. To stop
   the fake from becoming a second implementation that agrees with the code by
   construction, the fake **stores rows and folds them with the Go
   accumulators** — the same reference the parity test uses — so the package
   tests still exercise the fold semantics.
6. `internal/handlers/statuspages`, `badges`, `slos`, `availability`,
   `uptimereport` suites unchanged and green.

## Docs

- `wiki/features/results-aggregation.md`: the "every consumer relies on"
  section gains one line: consumers bucket **in the database**; the Go
  accumulators are the reference semantics.
- `wiki/conventions/database.md` if it lists the results indexes and who
  depends on them.
- Changelog entry (performance, user-visible: large status pages and badges
  load several seconds faster).

## Acceptance

- The slow-query log on the dev deployment no longer shows the
  `SELECT "result"."uid" … FROM "results"` statement above 1 s for the
  200-check page; the page view's total drops by ≥ 2.5 s (from spec
  `2026-09-22-04`'s baseline of 6.6–7.4 s).
- `make test-postgres` green.

## Out of scope

- Changing *when* hour rollups are produced (rolling up a closed hour
  immediately instead of at raw expiry). That is the structural fix that
  shrinks the raw scan from 24 h to 1 h for every consumer, and it deserves its
  own spec against `job_aggregation.go`.
- The response-time series (spec `2026-09-22-06`).

## Implementation Plan

Sequenced so each step lands green on its own.

1. **`models`: the filter, the result and the shared constants.**
   `internal/db/models/result_bucket.go` gets `ResultBucketFilter` (+ `Validate`,
   rejecting a mixed tier the way `RecentResultsPerCheckFilter.Validate` does),
   `ResultBucket`, `ProlepticEpochOffsetSeconds` (the 62135596800 the SQLite
   expression needs) and `SlowSampleThresholdMillis` — the threshold moves down
   to `models` because the SQL needs it and `models` cannot import `uptimebar`;
   `uptimebar.SlowSampleThresholdMillis` stays as an alias so no consumer moves.
   The filter carries an optional `PeriodStartBefore` on top of the spec's shape:
   `WindowAvailability` bounds its window on both edges
   (`ListResultsFilter.PeriodEndBefore`), and without it that call site cannot
   move off `ListResults` — see step 4.
2. **Bucket-origin expression + parity test first.** `resultBucketExpr` in each
   dialect package: `date_bin(make_interval(secs => ?), period_start,
   TIMESTAMPTZ '0001-01-01 00:00:00+00')` on Postgres, epoch-second arithmetic
   around `ProlepticEpochOffsetSeconds` on SQLite. A test feeds a few hundred
   random timestamps at 1 h / 24 h / 7 h through the expression and through
   `time.Truncate`, and asserts the same bucket start, per dialect.
3. **`AggregateResultBuckets` on both dialects.** Raw tier and rollup tier per
   the spec's SQL, status sets taken from the `models` constants, the tier side
   restated for the partial index. Built through the bun query builder (no
   `ORDER BY`) so the plan test can `EXPLAIN` the production statement the way
   `explainListResults` already does. Wired into the `db.Service` interface.
4. **Rewrite `uptimebar` onto the aggregate.** `ResultsLister` →
   `ResultBucketAggregator`. **Both** entry points move, not only
   `BucketAvailabilityInRegions`: `WindowAvailability` shares `listTier`,
   `rawRowCap` and `rollupRowCap` with it, so the deletions the spec lists by
   name are only reachable if the window path moves too — and the spec's own
   reasoning ("with an aggregate the output is bounded by `checks × buckets` by
   construction") applies to it identically. The window path asks for one bucket
   of the window's own span and folds every returned bucket into one
   `BucketStats`, which is exact: the bucket grid partitions the rows and every
   `BucketStats` field is an associative fold.
5. **Delete the cap machinery** (`rawRowCap`, `rollupRowCap`, `listTier`,
   `capMaxRegionsPerCheck`, `capSafetyMargin`, `capRateHeadroom`,
   `defaultRetentionHourDays`, `rawTierHours`, `windowDayCount`,
   `Hints.RawRowsPerHour`, `RawRowsPerHour`, `MeasureRawRowsPerHour`,
   `CheckRateLister`). `ListOrgCheckRates` **stays** — `entitlements` has four
   other callers. The five `uptimebarHints` helpers (statuspages, badges, slos,
   availability, uptimereport) lose their `RawRowsPerHour` line, and with it
   their now-unused `orgUID` parameter.
6. **Test fakes serve buckets by folding rows with the real accumulators**, so
   the package tests keep exercising `accumulateRaw`/`accumulateAgg` instead of
   agreeing with the code by construction.
7. **Docs**: `wiki/features/results-aggregation.md`,
   `wiki/conventions/database.md`, changelog.
