# Check detail: one chart line per region + Recent Results region filter with duration stats

## Problem

Follow-up to shipped specs **2026-07-05-02** (Recent Results region column)
and **2026-07-05-03** (chart region filter chips). Two gaps remain on the
check detail page (`/orgs/$org/checks/$checkUid`, e.g.
`/dash0/orgs/webingenia/checks/<uid>?graphPeriod=week`):

1. **The "All regions" chart view is still one merged series.** Spec 03 added
   per-region chips, but selecting "All" plots every region's points as a
   single `<Area>` (`web/dash0/src/components/checks/response-time-chart.tsx:593`,
   single series `:691-782`). For a check probed from regions with different
   baseline latencies (~60ms nearby vs ~700ms transatlantic) the merged line
   is still a sawtooth; the user must click chips one by one to compare
   regions. Spec 03 explicitly deferred this: "Multi-series All view (one
   colored line per region + legend + per-region tooltip) — natural follow-up
   once this ships."
2. **Recent Results shows the region but can't act on it.** The table renders
   a region Badge per row (`checks.$checkUid.index.tsx:991-1009`) but there is
   no way to narrow the list to one region, and no summary anywhere of how a
   region is doing (min/avg/max/p95 duration).

Facts that shape the solution:

- The chart already fetches every point with its region:
  `useAllResults(org, { checkUid, periodStartAfter, periodType, with:
  "durationMs,region", … })` (`response-time-chart.tsx:306-313`), where
  `periodType` mixes tiers by graph period — `raw` (hour/day), `raw,hour`
  (week, or day for sub-5-min checks), `raw,hour,day` (month) (`:286-295`).
- Post-03 internals: `ChartPoint.region` (`:42-49`), `selectedRegion` state
  (`:225-227`), stale-guard `effectiveRegion` (`:358-359`), client-side
  filter applied before gap detection (`:366-368`), chips rendered when
  `regions.length > 1` (`:558-583`), `graphRegion` URL param. Gap detection
  is per-tier median based (`:97-142`); status gradient stops color the
  single series green/red (`:474-507`, colors `:471-472`); dot clicks resolve
  the pinned result via `activeTooltipIndex` into `chartData` (`:515`).
- A five-color multi-series palette already exists as CSS variables
  `--chart-1` … `--chart-5` (`web/dash0/src/index.css:19-23` light,
  `:125-130` dark) — currently unused by this chart.
- The backend list endpoint already filters by region:
  `GET /orgs/:org/results?region=a,b`
  (`server/internal/handlers/results/handler.go:53-56`), combinable with
  `checkUid` + cursor pagination. The frontend `useResults` hook
  (`web/dash0/src/api/hooks.ts:647-687`) does **not** expose that param yet.
- Aggregated rollup rows (one row per period × region) already **store**
  `duration_min`, `duration_max`, `duration_p95`, `duration_avg`,
  `total_checks` (`server/internal/db/models/result.go:122-129`), but the API
  only exposes min/max (`with=durationMinMs,durationMaxMs`,
  `handlers/results/service.go:294-306`) — **no `with` token exists for
  p95 or avg**, so exact per-region stats for windows older than raw
  retention are stored yet unreachable.
- The aggregation job itself combines child buckets with
  totalChecks-weighted means (`server/internal/jobs/jobtypes/job_aggregation.go:342-382`),
  so a weighted-mean client-side combination has the same fidelity as the
  platform's own stored higher-tier numbers.

## Product decision

- **Chart (All regions, >1 region present): one line per region.** Each
  region gets a stable color from the `--chart-1..5` palette (cycle by
  sorted slug order). The existing region chips double as the legend: each
  chip gains a small color swatch matching its line. Selecting one region
  keeps today's behavior — single series with the green/red status gradient
  — so nothing changes for single-region checks or filtered views.
- In the multi-series view, line stroke = region color; down results stay
  visible as red dots on their line. The binary status gradient does not
  apply to multi-series (it would be ambiguous across overlapping lines).
- **Recent Results: server-side region filter + stats strip.** A region
  filter (All + one chip per observed region, emoji+name, same segmented
  Button pattern as the chart) sits in the Recent Results card header;
  clicking a row's region Badge selects that region too. Selection is a new
  `resultsRegion` URL search param, independent from `graphRegion` (the
  chart isolates, the table narrows — coupling them is a possible follow-up,
  not assumed here). The list refetches with `region=<slug>` so pagination
  stays correct.
- **Stats when a region is selected**: a compact strip in the card shows
  **min / avg / max / p95** duration plus the sample count for the selected
  region **over the chart's current `graphPeriod` window** (labeled, e.g.
  "last week"). One page, one time window — the stats reuse the exact
  dataset the chart already fetched (same react-query key → zero extra
  HTTP), rather than introducing a second window notion or a new endpoint.
- Stats fidelity is tier-aware: over raw rows the four numbers are exact;
  when the window includes rollup rows (week/month), min/max stay exact
  (min-of-mins / max-of-maxs), avg is the totalChecks-weighted mean, and p95
  is a totalChecks-weighted combination of bucket p95s (same method the
  aggregator uses) displayed with a `~` prefix.

## Proposal

### Backend (small): expose stored avg/p95

1. Add `durationAvgMs` and `durationP95Ms` optional fields to
   `ResultResponse`, populated for `with=durationAvgMs` / `with=durationP95Ms`
   from `result.DurationAvg` / `result.DurationP95` in `applyDurationFields`
   (`handlers/results/service.go:294-306`, tokens lowercased by
   `buildWithSet`). Naming mirrors the existing `durationMinMs`/`durationMaxMs`.
2. Document both tokens in `server/internal/app/openapi/openapi.yaml`
   (results `with` description + response schema — the docs-site API
   reference is generated from this file).
3. Table-driven handler/service tests: tokens present → fields returned on
   aggregated rows, absent on raw rows (nil in DB) and when not requested.

### Frontend — chart multi-series

4. Extend the chart fetch `with` to
   `durationMs,region,durationMinMs,durationMaxMs,durationAvgMs,durationP95Ms,totalChecks`
   (`response-time-chart.tsx:310`) — raw rows simply omit the aggregate
   fields, so payload growth is limited to rollup rows. Extract the fetch
   params (periodStartAfter + tier `periodType` + `with` + size) into a
   small exported helper so the route can issue the identical query for
   stats (same query key → shared cache).
5. When `effectiveRegion === null && regions.length > 1`, split the
   (unfiltered) points by region and render one `<Area>` per region, each
   with its **own `data` array** (recharts supports per-series `data` with a
   shared numeric time XAxis — the chart already uses `scale="time"` with an
   explicit domain `:627-637`). Per-series data keeps gap markers working:
   run the existing gap detection / `insertGapMarkers` per region series so
   one region's outage doesn't get bridged (`connectNulls` stays off).
   Y-domain/ticks keep being computed across all points (unchanged).
6. Stroke per region from `--chart-N` (sorted slug order, cycle if >5);
   `OrgResult.uid`-based dot click must switch from `activeTooltipIndex`
   lookup (`:515`) to reading the clicked series' `activePayload` payload
   (index-into-single-array no longer exists in multi-series mode). Tooltip
   shows `${emoji} ${name}` (fallback slug) alongside time/duration/status.
   The pinned-result box behavior is otherwise unchanged.
7. Chips gain a leading color swatch (small rounded square) in multi-series
   mode so they act as the legend; the "All regions" chip keeps no swatch.
   Dot-density threshold (dots only when ≤150 points, `:699-781`) applies to
   the **total** across series.
8. Single-region checks and single-region selection render exactly as today
   (status gradient path preserved).

### Frontend — Recent Results filter + stats

9. `useResults` gains a `region?: string` option mapped to the `region=`
   query param (`hooks.ts:647-687`); the Recent Results call
   (`checks.$checkUid.index.tsx:340-345`) passes the selected region.
10. New `resultsRegion` search param in the route's `validateSearch`
    (normalize to `string | undefined`), written with `replace: true`
    (incidental refinement). Stale-guard like the chart's: if the selected
    slug is not in the observed region set, treat as All.
11. Filter UI in the card header, rendered only when >1 region observed
    (region set derived from the shared chart-window dataset of step 4):
    segmented Buttons "All" + per-region `${emoji} ${name}` via
    `useRegions(org)` (`hooks.ts:2411-2426`, dedupes with the page's call).
    Clicking a row's region Badge sets `resultsRegion` to that slug (badge
    becomes a button; keep the Badge look, add hover/focus affordance).
12. Stats strip (visible only when a region is selected): min / avg / max /
    p95 + count, computed from the shared dataset filtered to that region,
    tier-aware as decided above (exact over raw; weighted combination when
    rollup rows are in the window, `~` prefix on p95, and on avg only if any
    rollup row lacks `durationAvgMs` — fallback to its plotted `durationMs`).
    Values format via the chart's ms/s convention (`formatMs`-style,
    `response-time-chart.tsx:83-86`). Layout: one compact wrapping row
    (mobile-friendly), labels from new i18n keys.
13. i18n: new keys in **all four locales** (en/fr/de/es `checks.json`),
    e.g. `detail.results.filterAll`, `detail.results.stats.min/avg/max/p95`,
    `detail.results.stats.samples`, `detail.results.stats.window.<period>`
    (reuse the wording of `resultDetail.durationMin/Max/P95` — "Min"/"Max"/
    "P95" — and add "Avg").
14. Design reference: the segmented-Button filter is already the documented
    pattern (time-range selector); add the **stats strip** and the
    **chip-with-color-swatch legend** to
    `web/dash0/src/routes/orgs/$org/design-reference.tsx` since both are new
    reusable patterns.

## Out of scope

- Coupling `graphRegion` and `resultsRegion` into one page-level region
  selection (possible follow-up once both exist).
- A dedicated server-side stats endpoint (client-side combination over the
  already-fetched window is enough here; revisit if a stats API is needed
  elsewhere, e.g. status pages).
- Stats for the "All regions" state (strip only renders for a selected
  region, per the request).
- Public status page (status0) charts.
- Per-region alerting/thresholds.

## Acceptance criteria

- Multi-region check, All regions, `graphPeriod=week`: the chart renders one
  visibly distinct line per region (distinct stroke colors from
  `--chart-1..5`), each chip shows the matching color swatch, and no
  cross-region sawtooth remains. Hovering shows the region (emoji+name) in
  the tooltip; clicking a dot still pins the correct result.
- Selecting a region chip: rendering identical to today's filtered view
  (status gradient, gap bands, y-rescale). Single-region checks: chart
  byte-for-byte behavior of today (no chips, no legend, gradient intact).
- A region-scoped outage renders a gap/break only on that region's line.
- Recent Results: choosing a region (header chip or row badge) narrows the
  table server-side (`region=` on the results call, pagination correct) and
  survives refresh via `?resultsRegion=`; "All" restores the unfiltered
  list. Stale `?resultsRegion=` deep links behave as All.
- With a region selected, the stats strip shows min/avg/max/p95 + sample
  count for that region over the current `graphPeriod`, labeled with the
  window; hour/day windows (raw tier) show exact values; week/month windows
  show `~`-prefixed p95. No additional HTTP request beyond the (extended)
  chart query and the filtered list query.
- Backend: `with=durationAvgMs,durationP95Ms` returns the stored aggregates
  on rollup rows, omits them on raw rows; OpenAPI documents both; existing
  `with` behavior unchanged.
- Mobile: chips wrap, stats strip wraps, table stays usable (no fixed
  widths).
- Playwright: multi-region chart (mocked results incl. two regions → two
  stroke colors present; chip swatches), table region filter (mock asserts
  `region=` reaches the API; row-badge click updates URL + list), stats
  strip (mocked raw + rollup rows → expected exact and `~` values).
  Single-region case asserts no chips/legend/strip. Mock patterns as in
  `web/dash0/e2e/check-chart-point-preview.spec.ts:18-37`.
- `make lint`, `make test` (backend), `make test-dash` green — no new eslint
  errors (pre-existing react-hooks debt stays out of scope).

## Implementation plan

- [ ] Backend: `durationAvgMs`/`durationP95Ms` response fields + `with`
      tokens in `applyDurationFields`; OpenAPI (`with` description +
      schema); handler/service tests.
- [ ] Chart: extend `with`, extract shared fetch-params helper; per-region
      series split with per-series gap markers; `--chart-N` strokes; chip
      color swatches; tooltip region line; dot-click via `activePayload`;
      preserve single-region/status-gradient path.
- [ ] Route/table: `resultsRegion` search param + stale guard; `useResults`
      `region` option wired to the query param; header filter chips;
      clickable row region badge.
- [ ] Stats strip: tier-aware min/avg/max/p95 + count over the shared
      chart-window dataset; `~` marking; period label; compact responsive
      layout.
- [ ] i18n keys in en/fr/de/es; design-reference entries for the stats strip
      and swatch-chip legend.
- [ ] Playwright coverage (multi-region chart, filter, stats, single-region
      absence); run `make lint` + backend tests + `make test-dash`.
