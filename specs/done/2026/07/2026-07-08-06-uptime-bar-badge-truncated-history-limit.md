# Uptime-bar badge only shows 1/2/3 days of history for 7d/30d/90d periods

## Problem

The uptime-bar badge renders data for far fewer days than its period, and —
tellingly — the number of populated days *grows with the period length*:

- `…/checks/dnsbl-acme-io/badges/status,uptime-bar?period=7d` → only **1**
  day populated (out of 7)
- same URL with the default `period=30d` → **2** days populated
- `period=90d&width=600` → **3** days populated

(Observed on `https://solidping.k8xp.com/dash0/orgs/acmetech/badges?check=dnsbl-acme-io&components=status%2Cuptime-bar`.)

The check has much more history than that, so all three should show the same
wall-clock span of data; earlier buckets render as "no data" gray instead.

### Root cause

`uptimebar.BucketAvailability`
(`server/internal/uptimebar/bucketing.go:104-165`) fetches the window's
rows with:

```go
filter := &models.ListResultsFilter{
    ...
    PeriodTypes: []string{raw, hour, day},
    PeriodStartAfter: &start,
    Limit: n * len(checkUIDs),   // <-- one row per bucket assumed
}
```

`Limit` is set to `n` (the number of *buckets*: 7, 30, or 90), but a single
day-bucket is fed by **many** rows: up to ~24 hourly rollup rows plus all
not-yet-rolled-up raw rows (one per probe per region — hundreds for a
frequent check), per the union-of-tiers design described in the package
comment (`bucketing.go:7-12`).

`ListResults` orders `period_start DESC`
(`server/internal/db/postgres/postgres.go:1742`; SQLite mirrors it), so the
query returns only the *newest* `n` rows. Those n rows span just the most
recent hours/days:

- 7d → 7 newest rows ≈ minutes-to-hours of raw data → 1 bucket
- 30d → 30 newest rows → reaches into yesterday's hourly rollups → 2 buckets
- 90d → 90 newest rows → ~3 buckets

which is exactly the observed 1/2/3-day pattern. The in-code comment on the
`Limit` ("at most n buckets per check") conflates buckets with rows.

### Blast radius

- The **status page** buckets through the same helper (that's the point of
  the shared package — `badges/service.go:242-247`), and there the limit is
  `n * len(checkUIDs)` shared across *all* checks of the page in one
  descending-ordered query, so a busy status page starves older buckets and
  possibly entire checks (rows are not distributed per check by the query).
- The 24h period (hourly buckets, `n=24`) is least affected but still wrong
  for multi-region checks (24 rows < 24 buckets × regions × probes).

## Solution

Stop assuming one row per bucket. Options, in order of preference:

1. **Aggregate in SQL** — add a dedicated bucketed-availability query
   (GROUP BY truncated `period_start` and tier-aware SUM of
   `total_checks`/`successful_checks` + raw-status counting) so the row
   count returned is exactly ≤ n per check regardless of raw density. This
   is the robust fix for both badge and status page and keeps one
   round-trip.
2. **Drop/raise the limit** — the window's row count is already naturally
   bounded by retention (raw ≈ last 24h, hour ≈ until day-rollup), so
   fetching without a limit (or with a generous safety cap, e.g.
   `n + 48 + rawRetentionRows` per check) is correct today, but the bound
   is implicit and per-region raw volume can still be large on the status
   page.

Either way:

- Fix or delete the misleading comment on the `Limit` in
  `bucketing.go:129-132`.
- Add regression tests in `server/internal/uptimebar/bucketing_test.go`:
  a check with more rows than buckets in the window (dense raw + hourly
  rollups) must fill *all* buckets that have data, for 7d/30d/90d; and a
  multi-check status-page-style call must not starve any check.
- Verify cross-surface parity stays intact (the existing
  `BucketAvailabilityForPeriod` seam, `badges/service.go:304-309`).

## Open questions

- Postgres and SQLite both implement `ListResults`; if going with option 1,
  the new bucketed query needs both implementations (see
  `sync-pg-to-sqlite` conventions).
- Does the aggregation job's deletion cadence guarantee a hard upper bound
  on raw rows per check per region that would make option 2 safe enough for
  the status page too?

## Implementation Plan

Chosen: **option 2, "drop the limit" variant (no cap at all)**, not a
generous-but-bounded cap. Rationale, from reading
`server/internal/jobs/jobtypes/job_aggregation.go`
(`calculateAggregationBoundary`): with *default* config, raw rows survive up
to `RetentionRaw` hours (24), but **hour-tier rows survive up to
`RetentionHour` **days** (30 by default)** before being rolled into `day` —
much longer than the spec's example bound (`n + 48 + rawRetentionRows`)
accounts for. A fixed "generous" cap sized off raw retention alone would
still silently truncate a busy multi-region check's hour-tier rows within a
30d/90d window — the same class of bug, just pushed further out. Since
`ListResultsFilter.Limit` already documents `0 = no limit`
(`server/internal/db/models/result.go:170`, same idiom as
`check.go:284`/`maintenance_window.go:54`), and the query is already
time-bounded by `PeriodStartAfter`, removing the row-count `Limit` entirely
is both the simplest change and the only variant of option 2 that's
correct under default retention settings. Residual operational risk (very
large multi-region/high-frequency checks on a busy status page returning a
large row count) is accepted and documented in code; option 1 (SQL
aggregation) remains the follow-up if this becomes a real bottleneck.

Steps:
1. `server/internal/uptimebar/bucketing.go`: delete the `Limit: n *
   len(checkUIDs)` line from the filter in `BucketAvailability` and replace
   the misleading comment (conflates buckets with rows) with one explaining
   the window is bounded by `PeriodStartAfter` + tier retention, not by row
   count.
2. `server/internal/uptimebar/bucketing_test.go`:
   - Update `TestBucketAvailability_MultiCheckSingleQuery`'s assertion from
     `Limit == 24*2` to `Limit == 0`.
   - Add a `dayRow` fixture helper (mirrors `hourRow` for `PeriodTypeDay`).
   - Enhance `fakeLister` to sort results `period_start DESC` and truncate
     to `filter.Limit` when `> 0`, mirroring real DB ordering — without
     this, the fake never exercises the bug (it currently returns the full
     fixture regardless of `Limit`), so today's test suite would stay green
     even if the `Limit` bug were reintroduced.
   - Add `TestBucketAvailability_DenseRowsFillAllBuckets` (table over
     7d/30d/90d): a check with far more rows (dense today-only raw rows +
     one day-rollup per day) than buckets must have every bucket in the
     window filled, not just the newest few.
   - Add `TestBucketAvailability_MultiCheckDoesNotStarveOlderChecks`: a
     status-page-style batched call with one dense/chatty check and one
     sparse-but-full-window check — the chatty check must not crowd the
     sparse check's older buckets out of the shared query.
3. `make fmt`, `make build-backend lint-back test`, fix until green.
4. No Postgres/SQLite query changes needed — `ListResults` already treats
   `Limit <= 0` as "no limit" in both backends, so this is a pure
   `uptimebar` package change.
