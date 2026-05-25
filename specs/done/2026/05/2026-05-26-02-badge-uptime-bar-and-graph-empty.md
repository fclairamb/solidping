# Fix empty uptime-bar and response-time-graph badges

## Problem

The badge endpoint renders `uptime-bar` as an all-grey strip and
`response-time-graph` as an empty frame for the default period (and every
standard period). Reproduce at:
`/dash0/orgs/default/badges?components=status,availability,uptime-bar,response-time-graph&check=snmp-sysdescr`

Root cause is in `fetchBucketData`
(`server/internal/handlers/badges/service.go`). It queries a **single**
aggregated `period_type` picked by `uptimeBarPeriodInfo`: `hour` for `24h`,
`day` for `7d/30d/90d`. But under the default retention config
(`aggregation.retention_raw=24h`, `retention_hour=30d`, `retention_day=12mo`)
aggregation rolls `raw → hour → day → month` and deletes the source rows, so
each tier covers a different, non-overlapping age band:

| age            | stored as |
|----------------|-----------|
| last 24h       | `raw`     |
| 24h – 30 days  | `hour`    |
| 30 days – 12mo | `day`     |

So the default **30d** badge asks for `day` rows that only exist for data
**older than 30 days** → the window is empty → all-grey bar / empty graph. The
**24h** badge asks for `hour` rows, but the last 24h is still `raw` → empty too.

(Verified live: `snmp-sysdescr` has 0 `day` rows and 0 `month` rows, but ample
`hour` rows and `raw` rows.)

A secondary defect: the bucket loop assigns `availMap[bucket] = *AvailabilityPct`
(and likewise for duration) — it **overwrites** rather than aggregates, so it
cannot correctly combine several finer-grained rows into one bucket.

## Fix

Rework `fetchBucketData` to build each bucket from **all available tiers**
(`raw` + `hour` + `day`) over the window, rolling them up in Go with proper
weighting. Because the tiers never overlap in time, unioning them cannot
double-count.

Steps (in `fetchBucketData`, `service.go`):

1. Keep `n` and `bucketDuration` from `uptimeBarPeriodInfo(period)`; stop using
   its period-type label for the query.
2. `bucketStart := now.Truncate(bucketDuration).Add(-n*bucketDuration)` (unchanged).
3. One query: `PeriodTypes: ["raw","hour","day"]`, `PeriodStartAfter: bucketStart`,
   `Limit: 0` (no SQL limit; bounded to ~2.2k rows for 90d under default retention).
4. Accumulate per bucket `key = result.PeriodStart.UTC().Truncate(bucketDuration)`
   into a small accumulator `{up, total int; durSum float64; durCnt int}`:
   - **raw** rows: skip `created`/`running` (mirror `calculateAvailability`);
     else `total++`, `up++` if `Status == ResultStatusUp`; if `Duration != nil`
     then `durSum += float64(*Duration)`, `durCnt++`.
   - **hour/day** rows: `total += *TotalChecks`, `up += *SuccessfulChecks`; if
     `DurationAvg != nil` then `durSum += float64(*DurationAvg) * float64(*TotalChecks)`,
     `durCnt += *TotalChecks`.
5. Emit `availMap[bucket] = float64(up)/float64(total)*100` when `total>0`, and
   `durationMap[bucket] = durSum/float64(durCnt)` when `durCnt>0`.

`buildBarSegments` and `buildGraphPoints` are unchanged — they already read those
maps keyed by bucket time, so existing bucket-alignment math is preserved. The
current (incomplete) bucket now fills from `raw`, so the latest segment is no
longer perpetually grey.

## Scope

- `server/internal/handlers/badges/service.go` — `fetchBucketData` only, plus a
  tiny per-bucket accumulator helper type.
- No SVG, frontend, or API changes.
- **Out of scope**: aggregation retention is by design (hourly kept 30 days);
  not changed here.

## Acceptance criteria

- For `snmp-sysdescr` at the default period, the uptime bar shows colored
  segments (red, since availability is 0%) for buckets that have data, and grey
  only for buckets predating the check's data.
- The response-time graph renders a non-empty area (~10s) for buckets with data.
- `24h`, `7d`, `30d`, `90d` all render filled bars/graphs from whichever tiers
  cover each bucket; buckets with genuinely no data stay grey / leave line gaps.
- Existing unit tests still pass (`buildBarSegments`, `buildGraphPoints`,
  `uptimeBarPeriodInfo` are untouched).
- New tests:
  - Unit test for the roll-up: mix `raw` + `hour` rows in the same day bucket;
    assert weighted availability and weighted average duration are correct.
  - Extend `server/test/integration/badges_test.go` to seed `raw`+`hour` rows
    and assert the bar has ≥1 non-grey rect and the graph emits a polyline/area
    path (not just the empty `#f5f5f5` frame).

## Verification

1. `make test` — new and existing badge tests pass.
2. Live (server on :4000):
   ```
   curl -s 'http://localhost:4000/api/v1/orgs/default/checks/snmp-sysdescr/badges/uptime-bar,response-time-graph'
   ```
   The uptime-bar row should contain at least one non-`#9f9f9f` rect, and the
   graph should contain a `<polyline>`/area `<path>` rather than only the empty
   `#f5f5f5` frame.
3. Visual check at
   `/dash0/orgs/default/badges?components=status,availability,uptime-bar,response-time-graph&check=snmp-sysdescr`.

## Implementation Plan

### Step 1 — Rework `fetchBucketData` in `service.go`

Replace the single-tier query with a multi-tier union query (`raw` + `hour` + `day`),
and accumulate per-bucket stats using a small helper struct rather than overwriting:

1. Remove the `periodType` variable from `uptimeBarPeriodInfo`'s usage in
   `fetchBucketData`; keep `n` and `bucketDuration` only.
2. Query with `PeriodTypes: []string{"raw","hour","day"}` and `Limit: 0`.
3. Define a private `bucketAccumulator` struct: `{up, total int; durSum float64; durCnt int}`.
4. Iterate results, key by `result.PeriodStart.UTC().Truncate(bucketDuration)`:
   - `raw` rows: skip `created`/`running`; else `total++`, conditional `up++`;
     accumulate `durSum`/`durCnt` from `Duration`.
   - `hour`/`day` rows: add `*TotalChecks`/`*SuccessfulChecks`; accumulate
     weighted `DurationAvg` (`durSum += float64(*DurationAvg) * float64(*TotalChecks)`).
5. Emit `availMap[bucket]` and `durationMap[bucket]` only when `total>0` /
   `durCnt>0`.

`uptimeBarPeriodInfo` is unchanged (still returns period-type string, but
callers stop using it for the query). `buildBarSegments` and `buildGraphPoints`
are unchanged.

### Step 2 — Unit test: multi-tier roll-up in `service_test.go`

Add `TestAccumulateBuckets` (package-level, within `badges` package):
- Mix `raw` + `hour` rows in the same bucket; verify weighted availability and
  weighted average duration.

### Step 3 — Integration test: seeded `raw`+`hour` rows in `badges_test.go`

Add `setupBadgesMultiTierData` + two new tests:
- `TestBadges_UptimeBarHasNonGreyRect`: seeds a `raw` result + an `hour`
  aggregation in the current bucket window; asserts the uptime-bar SVG contains
  at least one non-grey rect.
- `TestBadges_ResponseTimeGraphHasPolyline`: same seed; asserts the
  response-time-graph SVG contains a `<polyline>` or `<path>` (not just the
  empty frame).
