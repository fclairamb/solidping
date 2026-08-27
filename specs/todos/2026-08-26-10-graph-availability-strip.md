---
model: opus
effort: high
---

# Time-series graphs show when a check was down, but not what it cost — add an availability strip aligned to every response-time chart

## Problem

The dash0 response-time chart
(`web/dash0/src/components/checks/response-time-chart.tsx`) encodes up/down as
gradient color stops, which answers *when* something was down but not *how
much*: there is no quantitative availability anywhere on the graph itself.
"99.2% over this window, and here is the ugly hour" is the question people
actually bring to the page, and today it is answered only by the availability
table below the chart (`web/dash0/src/components/checks/availability-table.tsx`),
which uses fixed period tokens (`1h,24h,30d,90d,365d`) that ignore the chart's
range, drag-zoom and region filter — the two surfaces never line up.

status0 has the same gap in mirror image: the status page renders an
availability bar (`web/status0/src/components/shared/availability-bar.tsx`) and
a response-time chart
(`web/status0/src/components/shared/response-time-chart.tsx`) as two unrelated
widgets; the chart's own strip below the x-axis shows incident colors, not
availability percentages, and the availability bar's buckets are not aligned to
the chart's time slots.

Availability per time bucket is often more valuable than the response-time
curve itself, and all the ingredients already exist — rollup rows carry
`successful_checks` / `total_checks`
(`server/internal/db/models/result.go:171-245`), and a single canonical engine
(`server/internal/uptimebar/` — `BucketAvailability` in `bucketing.go`,
`WindowAvailability` in `window.go`) is already shared by status pages, badges
and the availability API precisely so they cannot disagree.

## Proposal

Add a **color-banded availability strip under the chart's x-axis** — one cell
per bucket (green / amber / red, gray for no-data), sharing the chart's
x-scale, window, zoom and region filter, with a tooltip giving the exact
percentage, up/total counts and downtime per bucket. A strip, not a second
y-axis line: an availability line is flat at 100% almost always and makes
99.9% vs 99.0% indistinguishable. The idiom already ships three times
(`web/dash0/src/components/ui/uptime-strip.tsx`, status0's
`availability-bar.tsx`, `renderUptimeBarRow` in
`server/internal/handlers/badges/svg.go:164`); the new part is syncing it to
the chart.

### Data source: canonical engine, not client-side math

Back the strip with `uptimebar.BucketAvailability` via a new bucketed endpoint
(e.g. `GET /api/v1/orgs/:org/checks/:check/availability/buckets?from=&to=&bucket=`)
or an extension of the existing availability handler
(`server/internal/handlers/availability/service.go`). Deriving buckets
client-side from the chart's already-fetched rows is tempting (rollup rows
already carry `availabilityPct`) but would duplicate the counting rules
(`models.Result.ExcludedFromAvailability()`, `result.go:260` — lifecycle
markers and `abandoned` excluded from both numerator and denominator, warning
counts as up) and drift from the availability table sitting right below.
Response shape per bucket: `periodStart/End`, `availabilityPct` + `hasData`
(mirroring `AvailabilityPct() (float64, bool)` — `ok=false` is "no data",
explicitly not 100%), `totalChecks`, `successfulChecks`.

### Bucketing: minimum one hour, scaled to the window

Buckets are multiples of **1 hour** — below that a percentage is noise (a
5-minute slice of a 1-minute check quantizes to 0/20/40/…%); at an hour a
1-minute check has ~60 samples and the number means something. The engine
already supports arbitrary hour-multiple buckets by summing hour rollups
(status pages map 24h→24×1h, 7d→7×24h — `statusPagePeriodInfo`,
`server/internal/handlers/statuspages/service.go:2204`), and the ladder aligns
with the chart's existing tier plan (`web/dash0/src/lib/chart-window.ts`: raw
24h → hour 7d → day 2mo).

Suggested mapping (keep cell counts legible, roughly 24–60 cells):

| Chart range | Bucket | Cells |
|---|---|---|
| hour (or any window ≤ ~3h) | no strip — show a single window-availability figure (`WindowAvailability`) as a header stat instead | 1 |
| day | 1h | 24 |
| week | 4h or 6h | 42 / 28 |
| month | 1d | ~30 |
| drag-zoom span | smallest hour-multiple keeping cells ≤ ~60 | — |

Exact widths are an implementation judgment call; the hard requirements are the
1-hour floor and that the strip re-buckets when the window changes.

### Semantics (decided up front, so the spec is the record)

- **Maintenance counts as normal probes**, matching the availability table,
  status pages and badges. Maintenance exclusion is deliberately SLO-only
  today (`ExcludingMaintenance()` called only from `slo/budget.go:134` and
  `slos/service.go`); the strip must not silently diverge from the table next
  to it. A later toggle can reuse the `maintenance_*` rollup columns.
- **No-data is a distinct gray state, never 100%.** Per-bucket availability
  cannot reach past day-tier retention (month rollups are not attributable to
  a single day — `uptimebar/bucketing.go:24-30`), so long windows get gray
  tails, same as the status page.
- **Regions**: when the chart's region filter is active, bucket that region
  alone; on "all regions", sum up/total across regions rather than averaging
  percentages — the `mergeBuckets` rule
  (`statuspages/service.go:2290`).
- **Colors**: reuse the status-page thresholds
  (`availabilityToStatus(pct, failures, upThreshold, degradedThreshold)` —
  see `specs/done/2026/08/2026-08-03-01-status-page-availability-color-thresholds.md`)
  rather than inventing a fourth green/amber/red mapping; dash0's
  `uptime-strip.tsx` has its own and should converge on the shared one.

### Phasing

1. **dash0 check detail** (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:1167`):
   strip under the response-time chart, wired to `graphPeriod` /
   `graphFrom/To` / `region` URL state, plus the new endpoint. Add the strip
   to the design reference page
   (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) per frontend
   conventions.
2. **status0 response-time chart**: align availability with the chart's time
   slots — either upgrade the existing incident strip below the chart to
   availability coloring, or render the shared strip component under it. This
   needs the pub status-page payload
   (`statuspages/service.go:736-780`) to carry buckets aligned to the
   response-time window rather than the page-level `dailyAvailability`.

### Testing

- Table-driven backend tests for the bucket endpoint: bucket-boundary
  alignment, tier seams (raw + hour + day mixed in one window), no-data
  buckets past retention, region summing, maintenance rows counted, lifecycle
  rows excluded — with positive controls proving each exclusion actually
  changes the result.
- Playwright coverage in `web/dash0/e2e/`: strip renders per range, re-buckets
  on drag-zoom, gray no-data cells, tooltip content, region filter parity with
  the chart.

## Resolved open questions

Answered by the maintainer on 2026-08-27, before implementation started.

**Q: Endpoint shape: new `/availability/buckets` route vs. a `bucket=` mode on
the existing availability handler (which is capped at 12 periods and
calendar-token oriented — probably a poor fit to stretch)?**

**Decision: a new route.** Add
`GET /api/v1/orgs/:org/checks/:check/availability/buckets?from=&to=&bucket=&region=`
and do **not** extend the existing availability handler.

The existing handler speaks a different language: `periods=1h,24h,30d` are
*trailing-window calendar tokens* plus a `tz`, capped at `maxPeriods = 12`
(`server/internal/handlers/availability/service.go:39`), and it has no
`from`/`to` at all (`server/internal/handlers/availability/handler.go:27`).
The strip needs an arbitrary window cut into ~24–60 uniform buckets, which is
precisely the shape of the engine call
`BucketAvailability(ctx, db, orgUID, checkUIDs, bucketDuration, bucketStart, n, hints)`
(`server/internal/uptimebar/bucketing.go:520`) — so the new route is a thin
wrapper, while a `bucket=` mode would be a second, incompatible query shape
bolted onto validation, a cap and a vocabulary that all assume the first one.
Cost accepted: one more endpoint in `server/internal/app/openapi/openapi.yaml`
and the generated client.

**Q: Whether the hour view's single-figure fallback lives in the chart header
or replaces the strip area, and the exact bucket widths for week/zoom spans?**

**Decision: the chart header, and the table below is binding.**

- Below ~3h, render the `WindowAvailability` figure as a **header stat on the
  chart**, and do not render the strip area at all. The header does not reflow
  when a drag-zoom crosses the 3h boundary, whereas a single full-width cell
  in the strip area reads as a broken 24-cell strip.
- Bucket widths are no longer an implementation judgment call — use: day → 1h
  (24 cells), week → 6h (28 cells), month → 1d (~30 cells), and for a
  drag-zoom span the smallest hour-multiple that keeps the cell count ≤ 60.
  The 1-hour floor stands in every case.

### Implementation note (not a question — flagged during the gate)

`uptimebar` sums across regions and exposes no per-region filter, so
delivering "region filter parity with the chart" will most likely mean
threading a region filter through the engine rather than filtering after the
fact. Whatever the shape, the summing rule on "all regions" stays as decided
above: sum up/total across regions, never average percentages.

## Implementation Plan

### Phase 0 — shared backend primitives

1. **Region filtering in `uptimebar`.** `BucketAvailability` / `WindowAvailability`
   keep their signatures (every existing caller is unchanged) and delegate to new
   `BucketAvailabilityInRegions` / `WindowAvailabilityInRegions` variants that take
   an extra `regions []string` and set `ListResultsFilter.Regions`. The filter field
   already exists and hour/day rollups preserve `region`
   (`job_aggregation.go:1205`), so a region-scoped bucket is a real per-region
   rollup read, not a post-hoc slice. `nil` regions = "all regions", which is the
   engine's existing sum-across-regions behaviour — the `mergeBuckets` rule.
2. **One classification, not a fourth.** Move the green/amber/red mapping into
   `uptimebar.Classify(pct, failures, upThreshold, degradedThreshold)` with the
   `StatusUp/Degraded/Down/NoData` vocabulary; `statuspages.availabilityToStatus`
   becomes a delegating one-liner so the status page, the badge strip and the new
   chart strip provably share one implementation (small-bucket guard included).

### Phase 1a — the new endpoint

`GET /api/v1/orgs/:org/checks/:check/availability/buckets?from=&to=&bucket=&region=`
in the existing `handlers/availability` package (new `buckets.go` +
`buckets_test.go`), registered next to the existing route in `app/server.go`.

- `from`/`to` are required RFC3339 instants, `to > from`, window ≤ the existing
  `maxLookbackYears` sanity bound.
- `bucket` is an optional Go duration. It must be a **whole positive multiple of
  one hour** (the spec's hard floor) and must not produce more than
  `maxBucketCells` (200) cells. When omitted the server picks the smallest
  hour-multiple keeping the count ≤ 60 — which is exactly the drag-zoom rule, so
  the client can simply not send one for a zoom.
- Buckets are **aligned**: `alignedStart = from.Truncate(bucket)` and
  `n = ceil((to − alignedStart) / bucket)`, because `BucketAvailability` keys on
  `periodStart.Truncate(bucketDuration)` (epoch-relative). Returning unaligned
  edges would make the cells disagree with the rows that fall in them.
- Response: `{ data: [...], bucketSeconds, windowStart, windowEnd, region }`, each
  cell `{ periodStart, periodEnd, hasData, availabilityPct|null, totalChecks,
  successfulChecks, status }`. `hasData=false` ⇒ `availabilityPct` null and
  `status:"noData"` — never 100.
- `window` carries the **exact** `[from, to)` fold from `WindowAvailability` (same
  cell shape) for the sub-3h header stat. It is a separate engine call rather than
  a sum of the cells because the cells are aligned outward and would over-count the
  hour view's edges; the two calls are fanned out with `errgroup`.
- Maintenance is **not** excluded — `BucketStats.ExcludingMaintenance()` is never
  called here, matching the availability table, status pages and badges.

### Phase 1b — dash0

- `src/lib/availability-strip.ts`: `bucketSecondsFor(spanMs)` (day→1h, week→6h,
  month→1d, otherwise the smallest hour-multiple with ≤60 cells) and
  `STRIP_MIN_SPAN_MS` (3h) — pure, unit-tested.
- `src/lib/availability-status.ts`: the TS twin of `uptimebar.Classify` plus the
  cell/dot colour classes. `ui/uptime-strip.tsx` converges onto it (it currently
  carries its own 100/0/else mapping).
- `src/api/hooks.ts`: `useCheckAvailabilityBuckets(org, checkUid, {from, to,
  bucketSeconds, region})`.
- `src/components/ui/availability-strip.tsx`: the presentational strip — one cell
  per bucket, tooltip with exact pct, up/total and the bucket's time span, gray
  `noData` cells, `aria-label`, `data-testid`s.
- `response-time-chart.tsx`: header stat (window availability, always rendered) +
  the strip under the x-axis, inset by the chart's own left gutter
  (`YAxis width=60` + the AreaChart default 5px margin) so cells sit under the plot
  area. The strip is not rendered at all when the window span < 3h. Window/zoom/
  region come from the chart's existing state, so the strip re-buckets on range
  change and on drag-zoom for free.
- `design-reference.tsx`: an `AvailabilityStripSection` with the import line and
  live green/amber/red/no-data cells.
- Locale keys in all four locales (en/fr/de/es).

### Phase 1c — tests

- Go table-driven `buckets_test.go` over a real in-memory SQLite service:
  boundary alignment, tier seams (raw + hour + day in one window), no-data cells,
  region summing vs. region filtering, maintenance counted, lifecycle/abandoned
  excluded — each exclusion paired with a **positive control** that moves the
  number when the excluded row is replaced by a countable one.
- `uptimebar` unit tests for the region-filtered variants and `Classify`.
- Vitest for `bucketSecondsFor` / the status mapping.
- Playwright `web/dash0/e2e/` coverage: strip renders per range, re-buckets on
  drag-zoom, gray no-data cells, tooltip content, region parity.

### Phase 2 — status0

Only after phase 1 is green: carry chart-aligned buckets in the pub status-page
payload (`statuspages/service.go:736-780`) and render the shared strip under
status0's response-time chart.
