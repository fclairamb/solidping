---
model: opus
effort: high
---

# Long availability windows silently under-report: the month tier is never read

## Problem

The availability computation is stale relative to the aggregation retention
defaults, causing long windows to silently under-report.

- Default aggregation retention is 24 raw hours / 7 hourly days / 2 daily
  months (`server/internal/jobs/jobtypes/job_aggregation.go`,
  `defaultRetentionRawHours/HourDays/DayMonths`). Day rows older than
  ~2 months are rolled into terminal `month` rows and the day rows deleted.
- `server/internal/handlers/availability/service.go` hardcoded
  `dayTierRetentionMonths = 12` and rejected lookbacks beyond it, assuming
  12 months of day-tier data exist — the pre-2026-07-14-02 default.
- `server/internal/uptimebar/window.go` (`WindowAvailability`) and
  `bucketing.go` (`BucketAvailability`) queried only `raw+hour+day` — the
  `month` tier was never read anywhere, so data older than the day-tier
  horizon was invisible. `window.go` comments still claimed
  "raw 24h, hour 30d, day 12mo".
- Net effect: on a default deployment, a `365d` or `ytd` availability window
  saw only ~2 months of data and reported availability computed over that
  subset, with no warning.

## Proposal

1. **Add the `month` period type to the `WindowAvailability` union.** The
   tiers are disjoint by construction (the aggregation job deletes source
   rows transactionally), so adding month cannot double-count, and the month
   tier is terminal — never rolled further, never deleted — so any lookback
   becomes answerable regardless of raw/hour/day retention tuning.
2. **Replace the 12-month lookback constant** in the availability endpoint
   and update the stale comments in `window.go`.
3. Tests proving a window spanning the day→month boundary counts the
   month-tier rows (seed day + month rows on either side of the boundary and
   assert totals).

## Decisions (as implemented)

- **`BucketAvailability` deliberately keeps `raw+hour+day`.** A month rollup
  spans many hour/day ticks and cannot be honestly attributed to any single
  one — truncating it to its `period_start` would dump a whole month's counts
  into one bucket. Ticks older than the day-tier horizon render "no data",
  which is the truthful answer at that granularity. Documented in the package
  comment and in the wiki page's Known gaps.
- **The lookback cap is an input-sanity bound, not retention-derived.** With
  the month tier in the union, retention no longer bounds what is answerable,
  so deriving the cap from retention config would wrongly reject long windows
  on default deployments. `maxLookbackYears = 10` only rejects absurd tokens;
  `parseDurationToken` additionally guards int64-nanosecond overflow so a
  token like `999999999d` errors instead of wrapping into a garbage window.
- **Test fidelity:** the shared `fakeLister` now honors `filter.PeriodTypes`,
  so the new boundary test genuinely fails without the union change
  (verified by temporarily reverting the fix — negative control).

## Implementation

Shipped in `a5a108af` (`fix(availability): long windows read the month tier
instead of silently under-reporting`) on `batch/2026-08-11`, alongside the
subsystem documentation in `wiki/features/results-aggregation.md`
(`dc03f63b`). 62 tests pass in the touched packages, 228 in the downstream
badges/statuspages consumers; lint clean.
