# Multi-region response-time chart hides errors on dense views

## Problem

Follow-up to shipped spec **2026-07-05-13** (one chart line per region on the
check detail page). The multi-series "All regions" view is much nicer to read
than the old merged sawtooth, but it lost error visibility:

- **Single-series mode** (one region selected, or a single-region check)
  paints failing stretches red via per-status gradient stops on the line
  stroke (`web/dash0/src/components/checks/response-time-chart.tsx:613-646`,
  applied through the `<linearGradient>` defs at `:791-802`). Errors are
  always visible regardless of point density.
- **Multi-series mode** (`isMultiSeries`, `:577`) strokes each region's
  `<Area>` with its flat palette color (`--chart-1..5`, `:586-592`,
  `:1041-1050`) — no gradient. Down results are only visible as **red dots**
  (`:1072-1079`), and dots are gated by the density threshold: they render
  only when the combined point count across all series is ≤ 150
  (`:601-608`). A Day view of a 1-minute check has ~1440 points per region,
  so dots are off and **a down result leaves no visible trace at all** —
  the chart looks green/healthy during an outage.

The user suggestions: give all regions one color that flips red when down,
or shade each region's color darker/redder when down — or something smarter.
A flat single color would throw away the per-region comparison the
multi-series view exists for, so the fix should keep region identity while
making failures unmissable.

## Proposal

Two complementary changes, both scoped to the existing chart component:

### 1. Per-series status gradient (red segments on each region's line)

Reuse the proven single-series mechanism per region: compute gradient stops
**per region series** (extract the stop-building logic at `:613-646` into a
helper taking a `ChartPoint[]`), using the region's palette color for
neutral statuses and `COLOR_DOWN` (`:611`) for failing ones
(`statusStyle(status).isDown` or `unknown` — same predicate as today,
`:617`). Render one `<linearGradient id="rt-region-<slug>">` per region in
the `<defs>` block and point each multi-series `<Area>`'s `stroke` (and
`fill`, keeping the current `fillOpacity={0.08}`) at its own gradient
instead of the flat color.

This is the "change the color when down" option done precisely: the failing
stretch of the affected region's line turns red while the rest of that line
— and every other region — keeps its identity color. Red means down,
uniformly across single- and multi-series modes.

Note the offsets: the single-series gradient computes stop offsets as index
fractions over *real* points (`(i - 0.5) / (n - 1)`), which assumes roughly
even x-spacing. That's the same assumption the shipped single-series path
already makes (including across mixed raw/hour tiers), so per-series reuse
is no worse; do not try to fix x-linearity in this spec.

### 2. Failure dots exempt from the density threshold

Even a red line segment can be a sub-pixel sliver when one bad sample sits
among 1400 good ones. Make failing points always render a dot, regardless
of `dotsEnabled`: in both the multi-series dot renderer (`:1052-1100`) and
the single-series one (`:942` area's dot prop), change the gate from
`dotsEnabled` to `dotsEnabled || isDownPoint(payload)`. Failing dots keep
`COLOR_DOWN` fill and stay clickable (they already cache into
`dotPositions` for the click-resolution path, `:648-676`). Failures are
rare by nature, so exempting only them keeps dense views clean while making
every error individually visible and pinnable.

Keep the existing behavior otherwise: neutral dots still respect the ≤150
threshold, selected-dot ring, activeDot rendering, tooltips.

## Out of scope

- The public status page chart (`web/status0`) — still single-series; align
  it later if/when it gains multi-series.
- Outage background bands (`ReferenceArea` shading of down windows) — a
  possible future enhancement, superseded here by red segments + dots.
- Fixing gradient-offset x-linearity for unevenly spaced points (pre-existing
  in single-series mode).

## Acceptance criteria

- Multi-region check, All regions, Day view (dots disabled): a region with
  down results shows a red stretch on **its own line** and a red dot per
  failing result; other regions' lines are unaffected. No failing result is
  invisible at any zoom level or density.
- The red used is the same `COLOR_DOWN` as single-series mode; neutral
  segments keep the region palette color, and region colors remain stable
  (sorted-slug assignment unchanged).
- Clicking a red failure dot pins that result (PinnedResultBox), including
  when total point count > 150.
- Single-region checks and single-region selection render exactly as today,
  except failure dots now also appear above the 150-point threshold.
- Warning/degraded ("up but something to report") statuses stay neutral —
  only `isDown`/`unknown` statuses go red, matching `:614-617`.
- Playwright: mocked multi-region dataset (>150 points, one region with a
  down span) asserts (a) red gradient stop present in that region's
  gradient def, (b) failure dots rendered and clickable, (c) the other
  region's line has no red stop. Extend the existing chart spec patterns
  (`web/dash0/e2e/`, e.g. check-chart-point-preview.spec.ts mocks).
- `make lint` and `make test-dash` green — no new eslint errors
  (pre-existing react-hooks debt out of scope).

## Implementation plan

- [ ] Extract the gradient-stop builder into a pure helper
      (`ChartPoint[] → stops`, neutral color parameterized); keep
      single-series behavior byte-identical.
- [ ] Multi-series: per-region `<linearGradient>` defs + wire each Area's
      stroke/fill to its gradient.
- [ ] Dot renderers (both modes): render failing dots even when
      `dotsEnabled` is false; verify click/pin path at high density.
- [ ] Playwright coverage per acceptance criteria; run `make lint` +
      `make test-dash`.

## Implementation Plan

1. **Extract `buildGradientStops(points, neutralColor, downColor)`** as a
   module-level pure function in `response-time-chart.tsx`, taking the
   already-filtered "real" points array (or the full `ChartPoint[]`, filtering
   internally) plus the neutral/down colors, returning the same
   `{ offset, color }[]` shape the inline `gradientStops` useMemo produces
   today. Introduce a single shared `isFailingStatus(status)` predicate
   (`status === "unknown" || statusStyle(status).isDown`) used by both this
   helper and the new `isDownPoint` dot predicate below — this also folds in
   the pre-existing single-series inconsistency where the gradient's
   `isNeutralStatus` didn't special-case `"unknown"` even though the dot
   renderers already did, and the code comment/acceptance-criteria both state
   "unknown" should go red. Rewire the existing single-series `gradientStops`
   useMemo to call the helper — verify offsets/colors unchanged for
   up/down-only data (no behavior change except the "unknown" edge case,
   which now matches the dots' existing convention).
2. Compute one `gradientStops` result per region (via `buildGradientStops`
   over `seriesByRegion[slug]`'s real points, using `colorByRegion.get(slug)`
   as neutral) in multi-series mode, render one
   `<linearGradient id="rt-region-<slug>">` per region in `<defs>`, and point
   each region `<Area>`'s `stroke`/`fill` at its own gradient (keep
   `fillOpacity={0.08}`).
3. Add `isDownPoint(payload: ChartPoint | undefined)` helper; change both dot
   render gates (single-series `~942`, multi-series `~1052`) from
   `!dotsEnabled ? false : (...)` to only skip when
   `!dotsEnabled && !isDownPoint(payload)`, i.e. the renderer function now
   receives every point when `dotsEnabled` OR the point is failing, and
   returns an invisible `<g/>` for genuinely-skippable neutral points at high
   density. Verify `dotPositions` caching and click/pin resolution
   (`handleChartClick`) still work when a lone red dot renders among a dense
   line with `dotsEnabled === false`.
4. `make fmt` after each step; commit per step.
5. Playwright: extend `web/dash0/e2e/check-detail.spec.ts` (mirrors the
   existing region-chip/multi-series describe block) with a >150-point
   two-region dataset where one region has a down span, asserting: a red
   `<stop>` exists in that region's `#rt-region-<slug>` gradient def; a red
   dot renders and is clickable (pins `PinnedResultBox`) despite the density
   threshold; the other region's gradient has no red stop.
6. `make build-dash0`, `bun run lint` (no new errors in touched files),
   run the new/extended E2E spec (side-car `SP_RUNMODE=test` server if the
   local devloop isn't in test mode).
