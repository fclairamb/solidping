---
model: sonnet
effort: high
---

# Status page response-time graphs mix all regions into one shared series

## Problem

On the public status page, each resource's response-time graph is a single
shared series that interleaves results from every region the check runs in.

- The backend loads the last 100 results per check with no region awareness:
  `fetchRecentResults` (`server/internal/handlers/statuspages/service.go:1949`)
  filters only by org + check UIDs, and `buildResponseTimeData`
  (`server/internal/handlers/statuspages/service.go:2142`) flattens whatever
  comes back into one `[]ResponseTimePoint` — `Result.Region` is dropped.
- The frontend renders that single series as one area chart per resource
  (`web/status0/src/components/shared/response-time-chart.tsx`).

For a check running in several regions (e.g. `eu2` at 40 ms and `us1` at
160 ms), the chart alternates between the two latencies and reads as a
meaningless saw-tooth. It also halves (or worse) the effective time window per
region, since the 100-result budget is shared across all regions' interleaved
raw/aggregated rows.

The data model already supports doing better: `results.region` is populated on
raw rows and aggregations are per period × region (one row per bucket per
region). The dash0 check-detail chart already renders per-region series
(`web/dash0/src/components/checks/response-time-chart.tsx` — per-region
dataKeys, `regionDisplayLabel` / `sortRegionSlugs` in
`web/dash0/src/lib/region-label.ts`); the public status page is the only
surface still collapsing regions.

## Proposal

Split the status page response-time data by region, end to end.

**Backend** (`server/internal/handlers/statuspages/service.go`):

1. Group the recent results per region instead of flattening. Change the wire
   shape from a flat `responseTimeData: []ResponseTimePoint` to per-region
   series, e.g.:

   ```json
   "responseTimeSeries": [
     { "region": "eu2", "points": [ { "time": "...", "durationP95": 42.1, "status": "up" }, ... ] },
     { "region": "us1", "points": [ ... ] }
   ]
   ```

   A result with `region = NULL` (single-region / legacy rows) goes into a
   series with an empty/absent region rendered without a region label. Keep
   emitting the legacy flat `responseTimeData` alongside for one release if
   external consumers may read this payload (the OpenAPI spec in
   `server/internal/app/openapi/openapi.yaml` documents it), or drop it if the
   status0 SPA is confirmed to be the only consumer — check and decide in the
   implementation.

2. Make the fetch budget per-region: `responseTimeLimit = 100` is currently
   shared across all interleaved regions, so N regions get ~100/N points each.
   Either raise the fetch limit proportionally to the org's active region
   count, or cap points per (check, region) after grouping so each region's
   series has a consistent depth.

3. Group resources (`fetchGroupRecentResults` picking `memberUIDs[0]`) must
   apply the same per-region grouping to whichever member's results they
   surface.

**Frontend** (`web/status0/src/components/shared/response-time-chart.tsx`,
`web/status0/src/api/hooks.ts`):

4. Update the `ResourceAvailabilityData` type and render one series per region.
   Recommended rendering: a single chart per resource with one line/area per
   region plus a small legend of region names — matching the dash0 precedent —
   rather than stacked per-region mini-charts, to keep the public page compact
   (mobile especially). When only one series exists (single-region check or
   legacy NULL-region data), render exactly what is shown today, with no
   legend.
5. The per-point incident strip under the chart (the colored status bar) should
   roll up across regions for a given timestamp (worst status wins), since it
   is an incident indicator rather than a latency series — or be dropped per
   region into the tooltip. Keep it one strip.

**Tests**: backend table-driven tests for multi-region grouping (including
NULL-region rows and group resources), plus a status0 Playwright/E2E or
component-level check that two regions render two series.

**Open questions** (decide during implementation, don't over-build):

- Availability bars (`dailyAvailability`) also aggregate across regions. The
  request here targets the response-time graphs; leave availability rollups
  shared unless splitting them falls out naturally — cross-region availability
  rollup is arguably the correct semantic for "is the service up".
- Check whether the embed widget (`web/status0/src/embed/`) consumes
  `responseTimeData`; if it does, migrate it in the same change.

## Implementation Plan

### Findings that resolve the open questions

- **Group resources**: `resourceRecentResults` (service.go:1934) already returns
  `nil` unconditionally for any resource with `CheckGroupUID != nil` (spec
  2026-08-01-03, "strict topology hiding") — there is no `fetchGroupRecentResults`
  function in the codebase; the name in this spec's context hints was stale.
  Point 3 of the Proposal is therefore already satisfied — no code change, just
  a renamed-field regression test to keep it pinned.
- **Legacy flat field**: `responseTimeData` is read ONLY by
  `web/status0/src/api/hooks.ts` + `status-page-view.tsx`. It is NOT read by
  `web/status0/src/embed/widget.ts` (a separate, lightweight `/summary`-based
  widget — zero references to `region`/`responseTime`/`availability` anywhere in
  that file) and it is NOT documented in `openapi.yaml` (`StatusPageResource`'s
  schema doesn't even include the `check`/`availability` fields the live handler
  actually returns — that whole runtime-enriched substructure is undocumented).
  Decision: **drop** the legacy field rather than keep it alongside a new one —
  status0 is confirmed the sole consumer, and nothing external depends on the
  wire shape. OpenAPI needs no update either way, since it never documented this
  substructure. (This deviates from the spec text's assumption that OpenAPI
  documents it — it doesn't; recorded here per instructions.)
- **Region display names**: status0 (public, unauthenticated) has zero
  region-name/emoji infrastructure — `RegionDefinition`/`useRegions` is
  dash0-only and org-scoped. The legend renders the raw region slug; fetching
  friendly names publicly is out of scope (would also leak org-configured custom
  region metadata to anonymous visitors).

### Backend (`server/internal/handlers/statuspages/service.go`)

1. Add `ResponseTimeSeries []ResponseTimeSeries` (`json:"responseTimeSeries,omitempty"`)
   to `ResourceAvailabilityData`, replacing `ResponseTimeData`. New type:
   `ResponseTimeSeries{ Region *string ``json:"region,omitempty"``; Points []ResponseTimePoint }`.
2. `fetchRecentResults`: return `map[string]map[string][]*models.Result`
   (checkUID → region key, `""` for NULL region → results), each region-bucket
   capped at `responseTimeLimit` independently. Raise the query's `Limit` by a
   documented `regionFanoutCap` multiplier (mirrors
   `uptimebar.capMaxRegionsPerCheck`'s "generous, never bites under realistic
   topology" reasoning) so one region's rows can't starve another's budget.
3. `resourceRecentResults`: return type becomes `map[string][]*models.Result`
   (one check's byRegion map). Group/multi-member guard unchanged.
4. `buildAvailabilityData` / `buildHourlyAvailabilityData`: `recentResults` param
   becomes `map[string][]*models.Result`; call new `buildResponseTimeSeries`
   which sorts region keys (empty string sorts first — NULL/legacy region comes
   first, no special-casing needed) and builds one `ResponseTimeSeries` per
   region via the existing, unchanged `buildResponseTimeData`.
5. Group resource: no code change (see Findings) — just re-point the existing
   `group_resource_test.go:419` assertion at the renamed field.

### Frontend (`web/status0`)

6. `src/api/hooks.ts`: replace `responseTimeData?: ResponseTimePoint[]` with
   `responseTimeSeries?: ResponseTimeSeries[]` on `ResourceAvailabilityData`
   (`ResponseTimeSeries { region?: string; points: ResponseTimePoint[] }`).
7. `status-page-view.tsx`: pass `avail.responseTimeSeries` to `ResponseTimeChart`
   (prop rename `data` → `series`).
8. `response-time-chart.tsx`: accept `series: ResponseTimeSeries[]`.
   - `series.length <= 1`: unchanged rendering (today's single Area, no legend).
   - `series.length > 1`: pivot into one combined array keyed by the sorted
     union of timestamps across series, one `Area` per region colored from the
     existing `--chart-1`..`--chart-5` palette (stable, sorted-slug order), a
     small flex-wrap legend (swatch + slug text, `translate="no"` on the
     dynamic slug), and the incident strip rolled up per shared timestamp
     (worst-status-wins: down > error > timeout > degraded/warning > other).
   - No zoom/drag/click interactivity added — status0's chart stays read-only.
9. i18n: only add a fallback label (`unknownRegion`, en/fr/de/es) if a NULL
   region series ends up mixed with named ones in practice — decide inline.

### Tests

10. Backend table-driven unit tests for `buildResponseTimeSeries`: two regions
    → two sorted series; NULL-region rows → one series with `Region == nil`;
    empty input → empty result.
11. Backend integration test (sqlite in-memory, mirrors
    `TestEnrichHourly_HealthyCheckReads100`): a check with 150 raw rows in
    region "eu2" and 150 in "us1" in-window → `ViewStatusPage` returns two
    series, each with exactly 100 points (proves the per-region budget fix).
12. Backend integration test: NULL-region-only rows (legacy data) → exactly one
    series with `Region == nil`.
13. Update `group_resource_test.go:419` for the renamed field.
14. status0 E2E (`web/status0/e2e/response-time-chart.spec.ts`, following
    `overall-status-badge.spec.ts`'s API-mock pattern — deterministic, no
    live-seed dependency): single-series payload → no legend; two-region
    payload → legend with two chips + two lines; mobile viewport (375px)
    doesn't overflow with 2 regions.

### QA gate

15. `make build-backend lint-back test`, then
    `make build-status0 && cd web/status0 && bun run lint` once, as the final
    gate.
