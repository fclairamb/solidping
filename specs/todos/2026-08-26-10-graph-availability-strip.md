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
