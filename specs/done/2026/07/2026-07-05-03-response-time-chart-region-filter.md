# Response Times chart: per-region filtering

## Problem

The check detail Response Times chart
(`web/dash0/src/components/checks/response-time-chart.tsx`) plots every
region's results as **one merged series**. For a check probed from regions
with very different baseline latencies (~60ms nearby vs ~700ms
transatlantic), the time-interleaved points render a sawtooth that reads as
instability, when each region is actually flat. There is no way to isolate a
single region.

Root-cause chain:

1. The fetch already has everything: `with: "durationMs,region"`
   (`response-time-chart.tsx:293`) and `useAllResults` follows cursors until
   exhausted (`web/dash0/src/api/hooks.ts:760`), so every point's region is
   in memory.
2. But `ChartPoint` has no region field (`response-time-chart.tsx:37-43`):
   the mapping only collects slugs into a `Set` for the subtitle and drops
   the per-point region (`:312-326`).
3. A single `<Area dataKey="durationMs">` renders the merged sequence. The
   merged data also feeds the status-gradient stops (`:417-450`) and the gap
   detector, whose per-tier median interval mixes all regions' cadences
   (`:95-133`) — both are less truthful on interleaved multi-region data.
4. The only region UI is a passive subtitle, "Showing all regions (…)"
   (`:501-505`, i18n key `detail.chart.showingAllRegions` in en/fr/de/es).

Supporting facts:

- Aggregated tiers stay per-region — the rollup job produces one row per
  period × region — so hour/day buckets can be filtered per region exactly
  like raw rows.
- The backend already supports comma-separated `region=` filtering on
  `GET /orgs/:org/results`
  (`server/internal/handlers/results/handler.go:53-56`) — available, but not
  needed here since all pages are fetched anyway.
- Region definitions (emoji + name) come from `GET /orgs/:org/regions` via
  `useRegions` (already used by the detail page).
- Chart settings already persist as URL search params: `graphPeriod` /
  `graphFull` in `validateSearch`
  (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:76-81`), wired
  through `initialPeriod` / `initialFullRange` / `onSettingsChange`
  (`:713-729`).

## Product decision

- Replace the passive subtitle with an **interactive region filter**: an
  "All regions" chip plus one chip per region present in the fetched data,
  labeled `${emoji} ${name}` (slug fallback). Rendered only when more than
  one region is present — same condition as today's subtitle — so
  single-region checks see no change.
- Selecting a region filters **client-side**: instant toggle, no refetch,
  and the existing single-series rendering (status gradient, gap detection,
  click-to-pin) is reused unchanged — it becomes *more* accurate on a single
  region's steady cadence.
- "All regions" keeps today's merged view (explicitly acceptable as a quick
  overview). Rendering one series per region in the All view is a follow-up,
  out of scope.
- The selection is part of the page's URL state, like the other chart
  settings: new `graphRegion` search param next to `graphPeriod` /
  `graphFull`.

## Proposal

1. `ChartPoint` gains `region?: string`; the mapping (`:315-326`) keeps it
   per point.
2. New chart state `selectedRegion: string | null` (null = all), seeded from
   a new `initialRegion` prop; `onSettingsChange` extended to
   `(period, fullRange, region)`.
3. Apply the filter **before** gap detection and full-range assembly inside
   the `useMemo` (`:298-407`), so gaps, gradient stops, y-domain, and ticks
   all operate on the filtered set. The chip list (`regionSet`) is still
   derived from the **unfiltered** data so chips don't vanish while
   filtered.
4. Stale-selection guard: apply the filter only when the selected region
   exists in the unfiltered region set; otherwise behave as All. This keeps
   a stale `?graphRegion=` deep link (regions changed, older data aged out)
   from silently emptying the chart.
5. Chips UI: reuse the small segmented-button pattern of the
   Hour/Day/Week/Month selector (`:481-497` —
   `Button variant={selected ? "default" : "outline"} size="sm"`), placed
   where the subtitle was (`:501-505`). Fetch definitions with
   `useRegions(org)` inside the chart (react-query dedupes with the page's
   call). Per frontend conventions, check the design reference first for an
   existing chip/filter primitive and add the pattern there if missing.
6. Route: extend `validateSearch` (`checks.$checkUid.index.tsx:76-81`) with
   `graphRegion: string | undefined`; the `onSettingsChange` navigate call
   (`:719-728`) writes it with `replace: true` (incidental refinement, like
   the existing params).
7. i18n: new key for the "All regions" chip (e.g. `detail.chart.allRegions`)
   in **all four locales** (en, fr, de, es); drop or repurpose
   `showingAllRegions`.
8. Edge cases:
   - Selected region has no points in the chosen time range → the chart's
     regular empty state, chips stay visible; no auto-reset.
   - Legacy points without a region: visible under All, not selectable.
   - The pinned result box keeps working: dot clicks resolve via
     `activeTooltipIndex` into the (filtered) `chartData` (`:455-465`).

## Out of scope

- Multi-series All view (one colored line per region + legend + per-region
  tooltip) — natural follow-up once this ships.
- Server-side `region=` filtering from the hooks.
- The Recent Results region column → spec **2026-07-05-02**.
- Public status page (status0) charts.

## Acceptance criteria

- Multi-region check: chips render (All + one per region, emoji+name);
  selecting a region shows only that region's points across tiers (raw +
  hour/day rollups) — no more cross-region sawtooth — and the duration axis
  rescales to that region's data.
- Deep link `?graphRegion=us-1` restores the selection; switching
  Hour/Day/Week/Month or Full range preserves it.
- Single-region check: no chips, rendering identical to today.
- All-regions default behavior unchanged.
- A filtered steady-cadence region shows no false gap bands.
- Playwright: multi-region case covered by mocking the results response
  (`page.route`), since local e2e runs a single region; the single-region
  case asserts chips are absent.
- `make lint` + `make test-dash` green (no new eslint errors).

## Implementation plan

- [ ] `ChartPoint.region` + keep region in the mapping; add
      `selectedRegion` state, `initialRegion` prop, extended
      `onSettingsChange`.
- [ ] Filter before gap/gradient/domain computation; chips derived from the
      unfiltered set; stale-selection guard.
- [ ] Chips UI + i18n keys (en/fr/de/es); consult the design reference and
      add the chip/filter pattern there if missing.
- [ ] Route search param `graphRegion` + navigate write-through.
- [ ] Playwright coverage (mocked multi-region + single-region chip
      absence).
- [ ] Run `make lint` + `make test-dash`.

## Implementation Plan

1. **`ChartPoint.region` + state/prop plumbing**
   (`web/dash0/src/components/checks/response-time-chart.tsx`)
   - Add `region?: string` to `ChartPoint` (:37-43).
   - Keep `r.region` per point in the `sorted.map(...)` mapping (:315-326),
     alongside the existing `regionSet.add(r.region)` (unfiltered — feeds the
     chip list later).
   - Add `ResponseTimeChartProps.initialRegion?: string`.
   - Extend `onSettingsChange?: (period: TimeRange, fullRange: boolean, region: string | null) => void`.
   - New state: `const [selectedRegion, setSelectedRegion] = useState<string | null>(initialRegion ?? null)`.
   - `updateRegion(region: string | null)` setter that also calls
     `onSettingsChange?.(timeRange, fullRange, region)`; call it from
     `updateTimeRange` / `updateFullRange` too so region survives when period
     or full-range toggle changes (mirrors how those two already round-trip
     each other).

2. **Region-aware filtering inside the `useMemo`** (:298-407)
   - Compute `regionSet` (unfiltered, from all `sorted` rows) before any
     filtering — this backs the chip list so chips never disappear when a
     filter is applied.
   - Stale-selection guard: `const effectiveRegion = selectedRegion && regionSet.has(selectedRegion) ? selectedRegion : null;`
     Use `effectiveRegion` (never the raw `selectedRegion` state) everywhere
     the filter is applied, so a stale `?graphRegion=` deep link silently
     behaves as All instead of emptying the chart.
   - Filter `data` down to `effectiveRegion` (when non-null) via
     `.filter(d => d.region === effectiveRegion)` immediately after building
     `data`, before it flows into either the `fullRange` branch or the
     non-full-range branch. This makes gap detection, gradient stops
     (derived from `chartData` downstream), y-domain (`min`/`max`/`span`),
     and tick computation all operate on the single-region series — the
     spec's core "less truthful on interleaved multi-region data" complaint.
   - `regions: Array.from(regionSet)` in both returned objects stays sourced
     from the unfiltered set (already true today — just confirm it isn't
     accidentally switched to the filtered set).
   - No change needed to `detectGaps`/`insertGapMarkers`/`gradientStops`
     internals — they already key off whatever `chartData` they're handed;
     region-awareness is achieved entirely by pre-filtering their input, per
     the spec's proposal (step 3).

3. **Chips UI** (replace the passive subtitle at :501-505)
   - Fetch `const { data: regionsData } = useRegions(org);` inside the chart
     (react-query dedupes with the page's own `useRegions(org)` call at
     `checks.$checkUid.index.tsx:347` — same query key, same org).
   - Build a `slug -> RegionDefinition` lookup from `regionsData?.regions`.
   - Render only `if (regions.length > 1)` (same gating condition as
     today's subtitle, using the unfiltered `regions` array) — a chip row:
     - "All" chip: `variant={effectiveRegion === null ? "default" : "outline"}`,
       label `t("detail.chart.allRegions")`, `onClick={() => updateRegion(null)}`.
     - One chip per slug in `regions` (unfiltered list, stable order):
       `variant={effectiveRegion === slug ? "default" : "outline"}`, label
       `${def.emoji} ${def.name}` falling back to the bare slug when no
       definition exists, `onClick={() => updateRegion(slug)}`.
   - Reuse the exact Button pattern already used for Hour/Day/Week/Month
     (`Button variant=... size="sm"` at :481-497) — no new primitive needed,
     matching the spec's "reuse the segmented-button pattern" directive. Not
     adding to the design-reference catalog since no new primitive is
     introduced (it's the same `Button` toggle pattern already documented
     there via the time-range selector); note this explicitly in the final
     report for the coordinator's audit.
   - Drop the old `showingAllRegions` subtitle block entirely (its i18n key
     becomes unused — leave the JSON key in place per spec step 7 ambiguity
     ["drop or repurpose"] — repurposing is riskier if anything else
     references it; grep confirms nothing else does, so it's safe to leave
     as dead JSON, matching how the pre-existing unused top-level
     `allRegions` key already sits unreferenced in the same locale files).

4. **Route wiring** (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`)
   - `validateSearch` (:76-81): add
     `graphRegion: typeof search.graphRegion === "string" ? search.graphRegion : undefined`.
   - Destructure `graphRegion` alongside `graphPeriod`/`graphFull` (:283).
   - Pass `initialRegion={graphRegion}` to `<ResponseTimeChart>` (:713-729).
   - Extend the `onSettingsChange` callback to `(period, full, region)` and
     write `graphRegion: region ?? undefined` into the `navigate` search
     object (:719-728), keeping `replace: true`.

5. **i18n** — add `detail.chart.allRegions` to all four locale files
   (`web/dash0/src/locales/{en,fr,de,es}/checks.json`), nested under
   `detail.chart` next to `showingAllRegions` (left in place, unused —
   see step 3). Suggested values: en "All regions", fr "Toutes les régions",
   de "Alle Regionen", es "Todas las regiones" (mirrors the wording/casing
   of the existing unrelated top-level `allRegions` key already in each
   file, for consistency).

6. **Playwright coverage** (`web/dash0/e2e/check-detail.spec.ts`)
   - New test: multi-region case. Mock `**/api/v1/orgs/*/results*` (pattern
     from `check-chart-point-preview.spec.ts:18-37`) with >=2 regions'
     worth of raw points (e.g. `region: "us-1"` cluster of low durationMs +
     `region: "eu-1"` cluster of high durationMs), mock
     `**/api/v1/orgs/*/regions*` with matching `RegionDefinition`s (emoji +
     name), mock `**/api/v1/orgs/*/incidents*` empty (pattern from the same
     file, :54-60). Assert: both region chips render with
     `${emoji} ${name}`, clicking one chip narrows the visible dot count
     (compare `.recharts-wrapper circle` count before/after, or assert via
     duration-axis / dot fill), the "All" chip is initially selected
     (`variant=default` — check via class or `aria-pressed` if the Button
     primitive exposes it, else assert visually-selected class), and the
     URL gains `?graphRegion=us-1` after selecting that chip.
   - New test: single-region case — mock results with only one `region`
     value, assert no chips render (`getByRole("button", {name: /All
     regions/})` not present) and the chart still renders identically to
     today.
   - If the local devloop isn't in `SP_RUNMODE=test` (test org 401s) so
     these can't run locally, author them anyway and report
     authored-but-not-run, per the QA instructions.

7. **Verification**: `make build-dash0`, `cd web/dash0 && bun run lint` —
   confirm zero new errors in the touched files (chart component, route
   file, locale JSONs, new/extended e2e spec). Pre-existing react-hooks
   debt elsewhere is out of scope.
