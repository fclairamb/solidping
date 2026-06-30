# Status page uptime ticks report "No data" where the badge shows data — unify the bucketing data source

## Context

The public status page renders a per-check availability bar made of "ticks":
24 hourly buckets for the `24h` period, or N daily buckets for `7d`/`30d`/`90d`
(shipped in `specs/todos/2026-06-30-03-status-page-24h-history-period.md`).

**The bug:** ticks render `noData` (light gray) for buckets that *do* have data.
Two surfaces disagree for the **same check and the same period**:

- `http://localhost:4000/status0/default/time-servers` → **"No data"** for buckets
  that should be filled.
- `http://localhost:4000/dash0/orgs/default/badges?check=ntp-google&period=24h&components=status,uptime-bar`
  → **the data is present** for that exact check + period.

The rendering is correct — `noData → bg-gray-300` is the intended treatment
([`status-style.ts:13`](../../web/status0/src/lib/status-style.ts:13),
[`availability-bar.tsx:13`](../../web/status0/src/components/shared/availability-bar.tsx:13)).
The defect is **upstream of rendering**: the data the status page feeds the bar
under-reports availability. The fix is to make the status page derive its bar from
the **same data source** the badge uses. **This is a backend-only change.**

A code comment already *claims* the two surfaces bucket identically —
*"mirrors the badges endpoint's `uptimeBarPeriodInfo` so the two surfaces bucket
identically"* ([`statuspages/service.go:953`](../../server/internal/handlers/statuspages/service.go:953)) —
but they don't. The bucket **boundaries** match; the **data fetched into them**
does not.

## Root cause — two divergences between the badge and the status page

### Divergence A — the data source (the "No data" bug)

- **Badge** (`fetchBucketData`,
  [`badges/service.go:309-353`](../../server/internal/handlers/badges/service.go:309)):
  runs **one** query over the window for `PeriodTypes: {raw, hour, day}`
  (`:323`), truncates each row to its bucket, and accumulates — raw rows via
  `accumulateRaw`, rolled-up rows via `accumulateAgg` (`:334-348`). **Every bucket
  that has *any* row (raw OR rollup) is filled.** Because the aggregation job
  deletes source rows after each rollup (`raw → hour → day → month`, see
  `server/CLAUDE.md`), the three tiers cover **non-overlapping age bands**, so
  unioning them never double-counts. Net effect: a bucket whose raw rows haven't
  been rolled up yet is *still filled immediately, from raw*.

- **Status page, hourly** (`enrichHourly`,
  [`statuspages/service.go:1111-1174`](../../server/internal/handlers/statuspages/service.go:1111)):
  fetches `PeriodTypes: {hour}` **only** (`:1121-1127`) and synthesizes **only the
  current, in-progress hour** from raw (`fillCurrentHourFromRaw`, `:1139`). Any
  *past* hour that has raw rows but no stored hourly rollup yet falls through to
  the `noData` default in `buildHourlyAvailabilityData` (`:1276`). The raw→hour
  rollup lags by `RetentionRaw` (~24h by default), so **the moment a check crosses
  an hour boundary, the previous hour reads "No data" on the status page** while
  the badge shows it. This is exactly the screenshot.

- **Status page, daily** (`enrichWithAvailability`,
  [`statuspages/service.go:1008-1098`](../../server/internal/handlers/statuspages/service.go:1008)):
  fetches `{day}` (`:1013`), synthesizes missing days from `{hour}` data
  (`synthesizeMissingDailyBuckets`, `:1052`), and fills **only today** from raw
  (`fillTodayFromRaw`, `:1058`). A recent *past* day that has only raw rows (before
  the raw→hour rollup ran) is missed → `noData`, while the badge fills it from raw.
  **Same class of bug, daily granularity.**

So the status page reconstructs buckets from **stored rollups + a single
synthesized current bucket**, whereas the badge reconstructs them from a
**raw+hour+day union over the whole window**. The union is strictly more complete,
which is why the badge "has the data" and the status page doesn't.

### Divergence B — the success rule (warnings)

Even where both fill a bucket from raw, they can compute **different percentages**:

- **Status page** raw synthesis uses `models.RawAvailability` →
  `ResultStatus.CountsAsUp()` = **Up + Warning**
  ([`result.go:74-96`](../../server/internal/db/models/result.go:74)). Warning is
  *"up with something to report"* and counts as success.
- **Badge** `accumulateRaw` counts **only** `ResultStatusUp`
  ([`badges/service.go:255`](../../server/internal/handlers/badges/service.go:255));
  Warning rows count toward the denominator but **not** as success, so warnings
  drag the badge's number down.
- **The aggregation job** counts Warning as up (`CountsAsUp()`,
  [`job_aggregation.go:747-751`](../../server/internal/jobs/jobtypes/job_aggregation.go:747)),
  so stored rollups — and therefore the badge's own `accumulateAgg` path
  ([`badges/service.go:266-279`](../../server/internal/handlers/badges/service.go:266)) —
  already treat warnings as success.

The badge is thus **internally inconsistent**: a warning lowers the bar while it is
a raw row, but stops lowering it once that row is rolled into an hourly bucket.
Unifying on `CountsAsUp()` fixes the badge's own inconsistency *and* aligns all
three computations (status page, badge, aggregator) on one rule.

## The key questions

### Q1 — Share the code, or replicate the badge's approach inside statuspages? **Share.**

Extract the badge's bucketing core into a small leaf package that both handlers
import. The "they bucket identically" contract is currently enforced only by a
comment — and that comment already drifted into a bug. Sharing the code makes the
contract structural, so the two surfaces *cannot* diverge again. (Replicating the
union query inside `statuspages` would fix the symptom but reintroduce the exact
duplication that caused it.)

### Q2 — Which success rule wins? **Up + Warning (`CountsAsUp()`).**

It is the canonical, tested model helper (`models.RawAvailability`), it matches the
aggregation job and `specs/done/2026/06/2026-06-30-02-status-page-availability-excludes-lifecycle-results.md`,
and it is already what the badge's rolled-up path does. The badge's *raw* path
changes to count warnings as up — a **visible, intended** badge behavior change
(warnings stop dinging the live uptime bar), flagged in the Risk log.

### Q3 — Scope: 24h only, or all periods? **All periods.**

Both the hourly and the daily status-page paths have the same divergence. Fixing
them on one shared helper keeps `24h`/`7d`/`30d`/`90d` consistent and is less code
than fixing only the hourly path.

### Q4 — Does the response-time chart change? **No.**

The status page's response-time series is a separate "last 100 raw rows" query fed
to `buildResponseTimeData`
([`statuspages/service.go:1589`](../../server/internal/handlers/statuspages/service.go:1589)).
Only the **availability** bucketing moves to the shared source; the response-time
path is untouched.

### Q5 — Performance for many checks over 90 days? **Bounded; add an explicit limit.**

Raw is retained ~24h (`RetentionRaw`), so the **raw tier of the union is always
bounded by retention regardless of the period** — older buckets come from
hour/day rollups, not raw. The badge already does this for 90d. Keep the status
page's existing single batched query across all of a page's checks, and add an
explicit `Limit` sized to `windowBuckets × len(checkUIDs)` so a busy page can't
fetch unboundedly.

### Q6 — Dead code? **Remove the now-unreachable synthesis helpers.**

Once the status page reads from the shared union, these status-page-only helpers
become unreachable and should be deleted to prevent re-drift:
`fillCurrentHourFromRaw`, `fillTodayFromRaw`, `synthesizeMissingDailyBuckets`,
`aggregateRawToHour`, and `aggregateRawToDaily` **iff** no other caller remains
(grep first — `RawAvailability` itself stays; it moves into the shared rule).

## Goal

The status page availability bar for a check renders the **same per-bucket
availability** as the badge uptime-bar for the same check + period, because both
derive from **one shared bucketing function** over the same `raw+hour+day` union
with the same `CountsAsUp()` success rule. In the screenshot scenario (a check a
few minutes/hours old), ticks are filled wherever raw data exists — matching the
badge — and `noData` remains only for buckets with genuinely zero rows.

## Non-goals

- **No rendering / color change.** The front end is correct; `noData` still renders
  gray for buckets that truly have no rows.
- **No change to the response-time chart** or its query (Q4).
- **No change to retention or aggregation timing.** We change *what the status page
  reads*, not when data is rolled up.
- **No new periods / bucket counts / anchoring.** Boundaries already match between
  the two surfaces; only the fetched data changes.
- **No frontend, no migration, no API-shape change.** `ResourceAvailabilityData` /
  `AvailabilityPoint` stay as-is; buckets just get correct statuses.

## Design (backend only)

### 1. New shared package — `server/internal/uptimebar/`

Move the badge's bucketing core here, generalized from one check to many:

```go
package uptimebar

// ResultsLister is the minimal db surface both services already satisfy.
type ResultsLister interface {
    ListResults(ctx context.Context, f *models.ListResultsFilter) (*models.ListResultsResponse, error)
}

type BucketStats struct{ Up, Total, DurCnt int; DurSum float64 }

func (b BucketStats) AvailabilityPct() (float64, bool) { /* up/total*100, ok=Total>0 */ }

// BucketAvailability runs ONE raw+hour+day query over [bucketStart, bucketStart+n*dur)
// for all checks and returns per-check, per-bucket stats. Buckets with no rows are
// simply absent from the inner map (the caller renders them as noData).
func BucketAvailability(
    ctx context.Context, db ResultsLister, orgUID string, checkUIDs []string,
    bucketDuration time.Duration, bucketStart time.Time, n int,
) (map[string]map[time.Time]BucketStats, error)
```

- One `ListResults` with `PeriodTypes: {raw, hour, day}`,
  `PeriodStartAfter: bucketStart`, `CheckUIDs: checkUIDs`, and an explicit `Limit`
  (Q5).
- Per `(checkUID, PeriodStart.Truncate(bucketDuration))` accumulate: raw rows via
  the **reconciled** `accumulateRaw` (counts `CountsAsUp()`, skips
  `IsLifecycleMarker()` — Q2); rollup rows via `accumulateAgg`
  (`SuccessfulChecks`/`TotalChecks`).
- Carry over the badge's `bucketAccumulator`, `accumulateRaw`, `accumulateAgg`,
  `buildBucketMaps` essentially verbatim, with the single success-rule change.

### 2. Badge refactor — `badges/service.go`

`fetchBucketData` becomes a thin single-check adapter over
`uptimebar.BucketAvailability(ctx, s.dbSvc, orgUID, []string{checkUID}, dur, bucketStart, n)`,
then projects the one check's inner map into the existing `availMap` / `durationMap`.
Badge output is identical **except** raw warnings now count as up (Q2), which also
makes the badge's raw and rolled-up buckets agree with each other.

### 3. Status page refactor — `statuspages/service.go`

- **`enrichHourly`**: replace the `{hour}`-only fetch + `fillCurrentHourFromRaw`
  with `uptimebar.BucketAvailability(ctx, s.db, orgUID, checkUIDs, time.Hour, bucketStart, 24)`.
  `bucketStart` is unchanged (`now.Truncate(time.Hour) - 23h`, `:1118`).
- **daily branch of `enrichWithAvailability`**: replace the `{day}` fetch +
  `synthesizeMissingDailyBuckets` + `fillTodayFromRaw` with
  `uptimebar.BucketAvailability(ctx, s.db, orgUID, checkUIDs, 24*time.Hour, historyStart, historyDays)`,
  where `historyStart = todayStartUTC - (historyDays-1) days` to match the loop in
  `buildAvailabilityData` (`:1548-1572`).
- **`buildHourlyAvailabilityData` / `buildAvailabilityData`**: change their input
  from `[]*models.Result` to the per-bucket `map[time.Time]BucketStats` for the
  check. For each bucket in the fixed window: if present → `AvailabilityPct` +
  `availabilityToStatus` (`:1619-1628`); if absent → `noData`. Keep emitting
  `Time`/`Date` and the weighted-overall calculation (`OverallAvailabilityPct`).
- Keep the separate response-time fetch + `buildResponseTimeData` unchanged (Q4).
- Delete the now-dead synthesis helpers (Q6).

### Files to create / modify

**New:**
- `server/internal/uptimebar/bucketing.go` — shared union-query bucketing + accumulators.
- `server/internal/uptimebar/bucketing_test.go` — unit + regression coverage.

**Modified (backend):**
- `server/internal/handlers/badges/service.go` — `fetchBucketData` → adapter; accumulators move to `uptimebar`; raw success rule → `CountsAsUp()`.
- `server/internal/handlers/badges/service_test.go` — update for the warning-counts-as-up change; keep period/anchor/color tests.
- `server/internal/handlers/statuspages/service.go` — `enrichHourly` + daily branch use the shared helper; `build*AvailabilityData` signatures; remove dead synth helpers.
- `server/internal/handlers/statuspages/service_test.go` — update `TestBuildHourlyAvailabilityData_*` and `TestEnrichHourly_*` for the new data path; add a past-bucket-filled regression test and a badge↔status-page parity test.

**New / migration / frontend:** none.

## Verification

Backend tests use `testify/require` + `t.Parallel()` (`server/CLAUDE.md`).

- **`uptimebar` unit:**
  - A check with only **raw** rows spanning the current **and** the previous hour →
    **both** buckets filled (direct regression for this bug).
  - Raw rows in a recent bucket + a stored `hour` row in an older bucket → both
    filled, **no double-count** (asserts the non-overlap union).
  - A `Warning` raw row counts as **up**; `created`/`running` lifecycle markers are
    excluded from the denominator.
  - A bucket with zero rows is **absent** from the map (→ caller renders `noData`).
- **Parity test:** the same synthetic `[]*models.Result` driven through the badge
  path and the status-page path yields **identical per-bucket pct** for `24h` and
  for a daily period.
- **Status page unit:** a past hour/day with raw-only data now reads its real
  status, not `noData`; for fully-rolled-up history the overall % matches the
  pre-change value (guards against a numbers regression).
- **Integration (`make dev-test`):** create `ntp-google`, let it run across an hour
  boundary; `GET /api/v1/status-pages/default/time-servers` (24h) shows the
  **previous** hour filled; cross-check against
  `/dash0/orgs/default/badges?check=ntp-google&period=24h&components=status,uptime-bar`
  — the two URLs from the bug report now **agree**.
- **E2E (optional):** status0 `web/status0/e2e/status-page.spec.ts` visual check
  that early buckets are no longer uniformly gray for a freshly-seeded check.
- `make build && make lint && make test`; `make fmt`.

## Risk log

| Risk | Mitigation |
|---|---|
| Badge uptime % shifts for checks with warnings (raw path now counts Warning as up) | **Intended** (Q2): aligns the badge's raw path with its own rolled-up path and the aggregation job; note in changelog; updated badge tests pin the new numbers |
| Union double-counts a bucket that holds both raw and rolled-up rows | Aggregation deletes source rows after each rollup → tiers are non-overlapping; explicit non-overlap unit test |
| 90d × many checks fetches too many rows | Raw tier bounded by ~24h `RetentionRaw`; add explicit `Limit`; keep the single batched per-page query |
| Cross-package extraction churn / import cycle | `uptimebar` is a leaf depending only on `models` + a tiny `ResultsLister` interface — no cycle with `badges`/`statuspages` |
| Removing synth helpers breaks another caller | Grep for callers before deleting; keep `RawAvailability` (it becomes the shared raw rule); keep `aggregateRawToDaily` only if still referenced |
| Status-page overall % regresses | For fully-rolled-up history the union yields the same numbers; parity + overall-unchanged tests guard it |

**Status**: Todo | **Created**: 2026-06-30 | **Depends on**: `2026-06-30-03-status-page-24h-history-period.md` (shipped) and `2026-06-30-02-status-page-availability-excludes-lifecycle-results.md` (shipped)

## Implementation Plan

1. **Extract `server/internal/uptimebar/`** from the badge's bucketing
   (`bucketAccumulator`, `accumulateRaw`/`accumulateAgg`, `buildBucketMaps`, and a
   multi-check `BucketAvailability`), with the raw rule reconciled to
   `CountsAsUp()`. Unit-test it (past-bucket-filled, non-overlap, warning,
   empty-bucket-absent).
2. **Refactor `badges.fetchBucketData`** onto the shared helper; update badge tests
   for the warning change.
3. **Refactor status page** `enrichHourly` + the daily branch onto the shared
   helper; change `build*AvailabilityData` signatures; remove the dead synth
   helpers; update status-page tests; add the badge↔status-page parity test.
4. **Integration-verify** against the two bug-report URLs; then
   `make build && make lint && make test` and `make fmt`.
