# Configurable aggregation retention

## Context

The aggregation job (`server/internal/jobs/jobtypes/job_aggregation.go`) compacts time-series results in three stages: `raw → hour → day → month`. The boundary that decides "this data is old enough to compact" is currently hardcoded in `calculateAggregationBoundary` (lines 238–254):

```go
case periodRaw:
    return now.Truncate(time.Hour), nil               // compact everything before the *current* hour
case periodHour:
    return startOfToday, nil                          // compact everything before *today*
case periodDay:
    return startOfThisMonth, nil                      // compact everything before *this month*
```

In practice this means:

- **Raw data lives at most ~1 hour.** As soon as the wall clock crosses an hour boundary, the previous hour's per-check raw points are collapsed into a single hourly point with avg/min/max/p95.
- **Hourly data lives at most ~1 day.** At midnight UTC, yesterday's 24 hourly points become a single daily point.
- **Daily data lives at most ~1 month.** On the 1st of each month, last month's days become a single monthly point.

The user's question: should this be configurable, globally and possibly per-org?

## Honest opinion

**Yes, it should be configurable, and the current defaults are too aggressive for a serious monitoring product.** Two separate problems:

1. **The defaults are bad.** Losing per-minute response-time detail after one hour is fine for synthetic checks running every 5 minutes; it's a real regression for HTTP checks running every 30s or 1m where users want to see 5-minute spikes from this morning. After 24h all hourly points collapse into a single daily avg — you can't see "this check is slower in the morning than in the evening". After 30 days you're left with one point per month, which is almost useless for capacity-planning conversations. Compare with Datadog (raw 15s ≈ 15 days), Prometheus (raw retention configurable, default 15d), Grafana Cloud (raw retention paid). Even 1m raw kept for 7 days would be a defensible default.

2. **The model itself is fine, the *parameters* are what's wrong.** "Compact everything older than the start of the current period of one rank up" is a perfectly clean rule — it always operates on whole completed periods, no half-aggregated buckets, easy to test. It just hardcodes "one period of headroom". Generalize to "N completed periods of headroom" and you keep all the simplicity:

   - `raw`: keep raw for the current hour + N-1 prior completed hours, then roll up.
   - `hour`: keep hourly for today + N-1 prior completed days, then roll up.
   - `day`: keep daily for this month + N-1 prior completed months, then roll up.

   N=1 is the current behavior. Recommended new defaults: **raw=24, hour=30, day=12** (24h of per-minute, 30d of per-hour, 1y of per-day, then per-month forever).

3. **Server-level config: yes. Per-org: probably not yet.** The user asked about both. My honest take: adding a koanf-level config (`SP_AGGREGATION_RETENTION_RAW=24` etc.) gives 90 % of the value for 10 % of the effort and pairs naturally with the existing `Config` struct. Per-org override is a real feature in a multi-tenant SaaS where some customers want longer retention and pay for it — but in a self-hosted single-tenant deployment (the most common solidping shape today), per-org adds a DB read on every aggregation tick and a UI surface to maintain, with no real customer pulling on the rope yet. **My recommendation: ship server-level first; add per-org only when there's a concrete request, behind the same parameter-table pattern that `regions` already uses.** This spec covers the server-level work and leaves a clearly-scoped follow-up for per-org.

4. **What this spec deliberately does not do** (and the user should push back if they disagree):
   - **No retroactive re-aggregation.** If you previously ran with raw=1h and now switch to raw=24h, the old hourly aggregates are not "un-compacted" — that data is lost. Only future aggregation runs see the new threshold. Fine and expected, but worth saying out loud.
   - **No retention-based deletion.** Right now nothing ever deletes monthly aggregates; the table grows forever. That's a real concern but it's a separate axis (data lifecycle) from compaction (data resolution). Punt to its own spec.
   - **No per-check override.** Per-check would actually be more useful than per-org (a check polled every 10s creates 360 raw rows/hour, vs one polled every 5m at 12 rows/hour — they have very different storage profiles). But per-check multiplies the boundary computation by the number of checks and currently the aggregation job processes one (check, region) pair per tick from a global query — adding per-check thresholds means the global query has to JOIN against checks and filter per-check. Worth it eventually, not in this spec.
   - **No change to the per-period roll-up granularity itself** (still raw → hour → day → month). Adding a 5-minute or 15-minute tier is a separate, larger discussion.

## Scope

Server-level configuration of how many completed periods of each tier to keep before rolling up:

- New koanf config block:
  ```yaml
  aggregation:
    retention:
      raw: 24    # hours of raw data to keep (current behavior: 1)
      hour: 30   # days of hourly data to keep (current behavior: 1)
      day: 12    # months of daily data to keep (current behavior: 1)
  ```
- Env-var override: `SP_AGGREGATION_RETENTION_RAW=24`, `SP_AGGREGATION_RETENTION_HOUR=30`, `SP_AGGREGATION_RETENTION_DAY=12`.
- The aggregation job reads these values when computing its boundary; everything else (the per-(check, region, period) discovery query, the in-Go aggregation math, the write+delete sequence) is unchanged.
- Defaults: **raw=24, hour=30, day=12**. This is a behavior change from today's `1/1/1`, intentional, and called out in the changelog.

Out of scope (own follow-up specs):
- Per-org override (sketch in "Future work").
- Per-check override.
- Retention-based deletion of monthly aggregates.
- New aggregation tiers (5m, 15m).
- Backfill / re-aggregation of already-compacted data.

## Approach

### 1. Config struct

In `server/internal/config/config.go`, add an `AggregationConfig`:

```go
// AggregationConfig controls how aggressively raw/hour/day data is compacted.
// Each value is the number of completed periods of that tier to retain before
// rolling up to the next tier. Minimum 1 (current behavior).
type AggregationConfig struct {
    RetentionRaw  int `koanf:"retention_raw"`  // hours of raw to keep (default 24)
    RetentionHour int `koanf:"retention_hour"` // days of hourly to keep (default 30)
    RetentionDay  int `koanf:"retention_day"`  // months of daily to keep (default 12)
}
```

Add it to `Config`:

```go
Aggregation AggregationConfig `koanf:"aggregation"`
```

Defaults block in `Load()`:

```go
Aggregation: AggregationConfig{
    RetentionRaw:  24,
    RetentionHour: 30,
    RetentionDay:  12,
},
```

Validation in `Validate()` — clamp each to a minimum of 1 with a clear error if the user sets 0 or negative:

```go
if c.Aggregation.RetentionRaw < 1 {
    return fmt.Errorf("aggregation.retention_raw must be >= 1, got %d", c.Aggregation.RetentionRaw)
}
// same for RetentionHour, RetentionDay
```

(Or silently clamp — argue against silent clamp because misconfiguration should be loud.)

### 2. Wire the config into the aggregation job

`AggregationJobDefinition` is currently stateless. The job receives a `*jobdef.JobContext` at run time which is constructed in `jobsvc`. Two options:

**Preferred** — pass an `AggregationSettings` struct into the job definition at construction time, mirroring how `Services` is wired:

1. In `server/internal/jobs/jobtypes/job_aggregation.go`, add fields to the definition:

   ```go
   type AggregationJobDefinition struct {
       RetentionRawHours   int
       RetentionHourDays   int
       RetentionDayMonths  int
   }
   ```

2. The factory `(d *AggregationJobDefinition) CreateJobRun(...)` passes those values into `AggregationJobRun`:

   ```go
   type AggregationJobRun struct {
       config             AggregationJobConfig
       retentionRawHours  int
       retentionHourDays  int
       retentionDayMonths int
   }
   ```

3. Where `AggregationJobDefinition{}` is registered today (search `&AggregationJobDefinition{}` in `server/internal/app/`), populate from `cfg.Aggregation`.

**Alternative** — read from `jctx.Services.Config` if the config is already exposed there. Check `server/internal/jobs/jobdef/types.go` for `JobContext` shape; if `Config` is reachable, prefer that — fewer plumbing changes. (At the time of writing the job context exposes `Services` and `DBService` — verify whether `Services.Config` exists; if not, add it, or pass the three ints through the definition struct.)

### 3. Boundary calculation

Replace the hardcoded `now.Truncate(hour)` etc. with a function that takes the retention values:

```go
// calculateAggregationBoundary returns the timestamp before which data of
// sourcePeriod is ready to be rolled up. With retention=N, the current
// (incomplete) period plus N-1 prior completed periods are kept; everything
// older is rolled up.
func calculateAggregationBoundary(
    sourcePeriod string,
    retentionRawHours int,
    retentionHourDays int,
    retentionDayMonths int,
) (time.Time, error) {
    now := time.Now().UTC()

    switch sourcePeriod {
    case periodRaw:
        return now.Truncate(time.Hour).Add(-time.Duration(retentionRawHours-1) * time.Hour), nil
    case periodHour:
        startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
        return startOfToday.AddDate(0, 0, -(retentionHourDays - 1)), nil
    case periodDay:
        startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
        return startOfMonth.AddDate(0, -(retentionDayMonths - 1), 0), nil
    default:
        return time.Time{}, fmt.Errorf("%w: %s", ErrInvalidSourcePeriod, sourcePeriod)
    }
}
```

Sanity check: with `retentionRawHours=1` the formula reduces to `now.Truncate(time.Hour)` — exactly the current behavior. Good — N=1 is bit-for-bit backward compatible, so anyone on an existing setup who explicitly sets the new env vars to `1/1/1` keeps the old behavior.

Update `aggregatePeriod` and `findAggregatableResults` (the only callers) to pass the values through. They live on `AggregationJobRun`, which now holds them as fields, so the call site just becomes:

```go
boundary, err := calculateAggregationBoundary(
    sourcePeriod,
    r.retentionRawHours,
    r.retentionHourDays,
    r.retentionDayMonths,
)
```

### 4. Tests

`server/internal/jobs/jobtypes/job_aggregation_test.go` already has `TestCalculateAggregationBoundary`. Update it:

- Add cases for retention values `> 1`, e.g. `raw=24` at fixed `now=2025-12-19 13:15` should produce `2025-12-18 14:00` (current hour 13:00 minus 23h).
- Keep the `1/1/1` cases as a backward-compat anchor — they should still pass with the same expected timestamps.
- Add a case for the boundary on month rollover (`now=2026-02-01 00:30`, retention=2 months → boundary = `2025-12-01`).
- Add a validation case for `retention=0` (if you choose error-on-zero) or for `retention=1` reducing to today's behavior.

### 5. Config documentation

Add to the existing config docs (search for where other `SP_*` env vars are documented — likely `docs/` and the README). Document the three new env vars with the same defaults the code uses.

### 6. Behavior-change notice

Because the new defaults compact less aggressively than the old ones, an existing deployment that upgrades will start using more storage. Surface this in the changelog/PR description with a one-liner: "Aggregation retention defaults raised from 1/1/1 to 24/30/12. Set `SP_AGGREGATION_RETENTION_RAW=1` etc. to keep the old behavior." No migration needed — old data is already compacted, the change only affects future writes.

## Verification

1. **Unit**: `go test ./server/internal/jobs/jobtypes/...` covers the boundary math.
2. **Manual integration**:
   - Run `make dev-test` (test mode generates synthetic data via `generate_data.go`).
   - With defaults (raw=24), confirm raw rows from the last 23 hours are still present after the job runs by querying `SELECT period_type, MIN(period_start), MAX(period_start), COUNT(*) FROM results GROUP BY period_type`.
   - Set `SP_AGGREGATION_RETENTION_RAW=1` and restart; on the next aggregation tick (or manually trigger via `/api/v1/orgs/$org/jobs`) confirm raw count drops to "current hour only".
3. **Validation**: start the server with `SP_AGGREGATION_RETENTION_RAW=0`; expect a clear startup error from `Validate()`.
4. **Config loading**: `go test ./server/internal/config/...` already exercises koanf — add a case asserting the new defaults.

## Future work (separate specs)

- **Per-org override** following the `regions` pattern: parameter keys `aggregation.retention.raw` etc. read via `db.GetOrgParameter`, falling back to system parameter, then to the koanf config. The aggregation job already runs per-org (it receives `OrganizationUID`), so the only new cost is one parameter read per tick — cheap and easy to cache. Worth doing the day a customer asks.
- **Retention-based deletion** of monthly aggregates (e.g., `aggregation.retention_month = 60` keeps 5 years and deletes older). Pairs naturally with this spec but is a different action (delete vs aggregate).
- **Per-check override** for high-frequency checks that need shorter raw retention to keep the table small.
- **Sub-hour aggregation tier** (5m or 15m) for users who want better-than-hourly resolution between "right now" and "yesterday".

---

## Implementation Plan

1. Add `AggregationConfig` struct to `server/internal/config/config.go` with `RetentionRaw/Hour/Day` fields and koanf tags; add a field on `Config`; set defaults `24/30/12` in `Load()`; validate each is `>= 1` in `Validate()`.
2. Add three corresponding system-parameter keys + apply funcs in `systemconfig.go` (env vars `SP_AGGREGATION_RETENTION_RAW`, `_HOUR`, `_DAY`) using the existing int coercion pattern.
3. Add `RetentionRawHours/HourDays/DayMonths` to `AggregationJobDefinition` and `AggregationJobRun`; populate from `cfg.Aggregation` at the registration site (search `&AggregationJobDefinition{` in `server/internal/app/`).
4. Generalize `calculateAggregationBoundary` to take the three retention values, with the formulas described in the spec; update both callers (`aggregatePeriod`, `findAggregatableResults`) to pass them through.
5. Update existing tests in `job_aggregation_test.go` to feed the retention args; add cases for `retention>1` including the month-rollover edge.
6. `make build-backend lint-back test` and fix anything that breaks.
