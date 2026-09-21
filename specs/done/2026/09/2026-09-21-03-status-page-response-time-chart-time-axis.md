---
model: opus
effort: high
---

# The status page response-time chart plots time as a category, so two adjacent points can be 39 days apart

## Problem

On a public status page, the response-time chart for `univ-avignon (s3ns)` renders a plot
whose x-axis reads `1 juil.` · `21 sept.` · `11:44` · `11:49` · `11:54`. Hovering the last
point of the amber area gives `13 août 2:00 — default 80ms`; hovering the first point of the
green line, immediately to its right, gives `21 sept. 11:38 — @s3ns-paris-prod 2ms`. Two
neighbouring columns are **39 days apart**, while the remaining 80% of the plot width covers
**16 minutes**. The card above it advertises a 30-day window (`il y a 30 jours` →
`Aujourd'hui`), so the chart is also showing ~82 days of history that the availability bar is
not.

Three independent defects stack up to produce this.

### 1. The x-axis is a category axis, so spacing ignores time

[`response-time-chart.tsx:463-473`](web/status0/src/components/shared/response-time-chart.tsx:463)
(multi-series) and [`:332-342`](web/status0/src/components/shared/response-time-chart.tsx:332)
(single-series) declare:

```tsx
<XAxis dataKey="time" ticks={ticks} tickFormatter={...} interval="preserveStartEnd" />
```

No `type="number"`, no `scale="time"`. Recharts therefore treats `time` as a **category**:
every row gets an equal slice of the plot width regardless of its timestamp. A 39-day gap and
a 20-second gap draw identically. This is also why `pickTicks`
([`:48-56`](web/status0/src/components/shared/response-time-chart.tsx:48)) produces a nonsense
axis: it samples five evenly spaced *rows*, not five evenly spaced *instants*, and
`buildTickLabels` ([`:61-85`](web/status0/src/components/shared/response-time-chart.tsx:61))
then demotes repeated days to a time-of-day label, so the axis ends up reading like the whole
chart covers one morning.

dash0's equivalent chart already does the right thing:
[`web/dash0/src/components/checks/response-time-chart.tsx:1119-1128`](web/dash0/src/components/checks/response-time-chart.tsx:1119)
uses `dataKey="ts"` with `type="number" scale="time" domain={[domainMin, domainMax]}`. status0
never got the same treatment.

### 2. The backend series are "newest 100 points per region", with no shared window

`fetchRecentResults`
([`service.go:3054-3110`](server/internal/handlers/statuspages/service.go:3054)) fetches, per
`(check, region)` pair, the newest `responseTimeLimit = 100` points
([`:3008`](server/internal/handlers/statuspages/service.go:3008)) over a rollup span of
`2 * 100 * 24h = 200 days` ([`:3020`](server/internal/handlers/statuspages/service.go:3020)).
`trimResponseTimeSeries` ([`:3129-3155`](server/internal/handlers/statuspages/service.go:3129))
then truncates each region to its own newest 100, independently of every other region.

Nothing ties those series to a common window, and nothing ties them to the page's own
`HistoryDays`, which *is* used for the availability bar
([`:2893-2901`](server/internal/handlers/statuspages/service.go:2893)).

The consequence is exactly the screenshot:

| Region | Tier of its 100 points | Span covered |
|---|---|---|
| `default` (stopped reporting ~13 Aug) | day rollups | 1 Jul → 13 Aug |
| `@s3ns-paris-prod` (live, 1-min check) | raw | last ~16 min |

The two series are **disjoint in time**. A region that has been dead for five weeks still
donates six weeks of ancient rollups to a chart that claims to show 30 days.

### 3. `buildCombinedRows` derives its slot tolerance from the finest series

[`response-time-rollup.ts:74-96`](web/status0/src/lib/response-time-rollup.ts:74) takes the
**minimum** median sampling interval across all series, so with one raw 1-minute series present
the tolerance is ~54s. Day-rollup points from `default` can never merge with anything and each
becomes its own row. That is defensible in isolation, but it means row count (and therefore,
under a category axis, plot width) is dominated by whichever series samples fastest.

### Secondary symptoms visible in the same screenshot

- A third legend entry, `Région inconnue` (blue), renders as a **single isolated dot at 0ms**
  at the far left. `responseTimePointsHaveSignal`
  ([`service.go:3426-3434`](server/internal/handlers/statuspages/service.go:3426)) drops a
  region group only when every point has a `nil` duration. A NULL-region row carrying a literal
  `0` survives and manufactures a phantom one-point series plus a legend entry.
- The availability strip under the chart has the same flaw by construction: one `flex-1` cell
  per chart row ([`:236-264`](web/status0/src/components/shared/response-time-chart.tsx:236)),
  so six weeks of `default` collapse into the thin amber sliver on the left while 16 minutes of
  `@s3ns-paris-prod` occupy the rest.

## Proposal

Fix the data window on the server and the axis on the client. Either one alone leaves a wrong
chart: a real time axis over disjoint series draws one cluster at each end with 39 days of
white space between them, and a correct window over a category axis still mis-spaces uneven
sampling.

### A. Server: bound the response-time series to the page's own window

1. Add a lower time bound to `fetchRecentResults` derived from `page.HistoryDays`, the same
   value the availability bar uses (`service.go:2893-2901`). The rollup branch's `Since`
   becomes `max(now - HistoryDays, now - responseTimeRollupSpan)` rather than the flat 200-day
   `responseTimeRollupSpan`. Keep `responseTimeRollupSpan` as the hard ceiling so an absurd
   `HistoryDays` cannot reintroduce an unbounded fetch.
2. Once bounded, pick the **tier** from the window instead of taking "whatever the newest 100
   rows happen to be": a 30-day window wants day (or hour) rollups for every region, not raw
   for the live one and day rollups for the dead one. Mixing a raw series and a day-rollup
   series on one chart is not meaningful even when both are in-window. Decide one target
   granularity per resource from `HistoryDays` and the 100-point budget, and request that tier
   for every region.
3. Drop a region series with **no point inside the window**. A region retired five weeks ago
   must not appear in the legend of a 30-day chart at all.
4. Tighten `responseTimePointsHaveSignal` (or the point builder) so a lifecycle marker carrying
   a literal `0` duration is treated like a `nil` one, killing the phantom `Région inconnue`
   series.

### B. Client: make it a real time axis

1. In both branches of
   [`response-time-chart.tsx`](web/status0/src/components/shared/response-time-chart.tsx),
   carry an epoch-ms field on each row and switch the `XAxis` to
   `type="number" scale="time" domain={[min, max]}`, matching
   [dash0's chart](web/dash0/src/components/checks/response-time-chart.tsx:1119).
2. Replace `pickTicks` / `buildTickLabels` with tick selection over the **time domain** (evenly
   spaced instants, labelled at a granularity derived from the domain span), so the axis can
   never print `21 sept. · 11:44 · 11:49`.
3. Keep `buildCombinedRows`. With `connectNulls={false}`
   ([`:160-164`](web/status0/src/components/shared/response-time-chart.tsx:160)) each series
   still needs its consecutive points in adjacent rows, so the slot merge is load-bearing even
   on a numeric axis. Revisit `samplingInterval`'s min-across-series rule only if step A.2
   leaves series at genuinely different granularities.
4. Make the availability strip time-proportional: give each cell a `flex` weight proportional
   to its slot duration instead of a uniform `flex-1`, so a cell sits under the slice of plot
   it actually describes (which is what the component's own docstring already claims).

### Tests

- Unit (`web/status0/src/lib/response-time-rollup.test.ts` + a new chart-level test): a series
  set whose regions cover disjoint time ranges must produce a domain and tick set spanning the
  real interval, and must not place two points 39 days apart in adjacent columns.
- Go (`server/internal/handlers/statuspages/`): a check with one live region and one region
  whose last point predates `HistoryDays` returns **one** series; points older than
  `HistoryDays` never appear; a NULL-region 0ms marker yields no series.
- Playwright (`web/status0/e2e/response-time-chart.spec.ts`): seed a resource with a retired
  region plus a live one, assert the legend has exactly one entry and the axis labels are
  monotonic in time.

## Resolved open questions

- **Q: Should the response-time window be hard-locked to `HistoryDays`, or should the chart get its own (shorter, e.g. 24h/7d) window with a selector?**
  **Decision:** Hard-lock the window to `HistoryDays` — no selector. The window is a rolling span ending at **now**, matching the availability bar's span (`todayStart − (HistoryDays−1)` → now, since the bar's newest bucket is today). This matches the ecosystem convention (status pages render history over the page's configured history window) and dash0's own convention. **The current (incomplete) day must be calculated as well**: today's points come from raw probes (and hour rollups as they close), never waiting for a day rollup — the day rollup under default retention only materializes ~7 days later (`calculateAggregationBoundary`, `job_aggregation.go`). Do not rely on day rollups for anything newer than what the aggregator has actually closed; request raw alongside rollups for the recent seam, exactly as dash0 does.
- **Q: When a region legitimately has a gap inside the window (worker restart, paused check), confirm the numeric axis plus `connectNulls={false}` still renders it as a gap rather than a straight line, and that `isolatedDot` still fires for a truly isolated sample.**
  **Decision:** Treated as an acceptance criterion, not a design choice. The implementation must verify with unit tests (in `web/status0/src/lib/response-time-rollup.test.ts` and/or a chart-level test) that (a) an in-window gap between two points of one region renders as a gap (no connecting line across the gap) and (b) a truly isolated sample still triggers `isolatedDot`. Keep `buildCombinedRows` intact; if gap semantics need a slot-expansion rule to survive the numeric axis, implement it.

## Implementation Plan

### A. Server — bound the window and equalize tiers (`server/internal/handlers/statuspages/service.go`)

1. **New `fetchRecentResults` parameter.** Signature gains `windowStart time.Time`
   (zero = unbounded, the pre-spec behavior — kept as the fallback the parity
   tests drive). Callers:
   - `enrichWithAvailability` passes the same `historyStart`
     (`todayStart.AddDate(0, 0, -(HistoryDays-1))`) the availability bar uses.
   - `enrichHourly` passes its 24 h `bucketStart`.
2. **Bounded tiers (A.1).** `rollupSince = max(windowStart, now -
   responseTimeRollupSpan)` — `responseTimeRollupSpan` stays the hard ceiling so
   an absurd `HistoryDays` cannot unbound the fetch. The raw branch's `Since`
   becomes `RawTierStart(rollupSince, now, hints.RetentionRawHours)` — the same
   clamp, now also capped by the page's own window.
3. **Tier equalization (A.2) — design note.** Requesting ONE tier for every
   region is impossible as literally specified: under default retention day
   rollups only exist for days ≤ today−7 (`calculateAggregationBoundary`,
   `retention_hour=7`), so a day-tier-only fetch leaves a 7-day hole at the
   window's right end, and the open day exists only as raw. The equalization is
   therefore achieved the way dash0's seam fetch does it, server-side:
   every region is subject to the SAME window and the SAME tier budget
   allocation, so a live region can no longer donate 100 raw minutes while a
   dead one donates day rollups. The per-region point budget is split across
   the tier buckets (raw seam / hour rollups / day+month rollups)
   proportionally to the time span each tier actually covers inside the
   window; within a tier the kept rows are evenly spaced over that tier's
   span (newest and oldest always kept). Total points per region stay ≤
   `responseTimeLimit`. `trimResponseTimeSeries` grows a windowed variant;
   the zero-window path keeps the exact legacy trim (parity tests pin it).
4. **Drop out-of-window regions (A.3).** After the windowed trim, a region
   whose series is empty is deleted from the map, so `buildResponseTimeSeries`
   never sees it and a region retired before the window never reaches the
   legend.
5. **No phantom series from a 0 ms marker (A.4).** In `buildResponseTimeData`,
   a row whose status is excluded from availability (`created`, `running`,
   `abandoned`) gets `DurationP95 = nil` — a lifecycle marker carrying a
   literal `0` is treated like a nil one. `responseTimePointsHaveSignal` also
   treats an all-zero series as no signal.

### B. Client — numeric time axis (`web/status0/src/components/shared/response-time-chart.tsx`)

1. New pure module `web/status0/src/lib/chart-axis.ts`:
   `computeTimeAxis(times: number[], maxTicks)` → `{ domain: [min, max],
   ticks: number[] }` picking evenly spaced instants over the TIME domain, and
   `formatAxisTick(ms, spanMs, locale)` with dash0's adaptive granularity
   (sub-hour → time-of-day, multi-day → date). Unit-tested with bun:test,
   including the disjoint-39-days fixture (domain and ticks must span the real
   interval; no two adjacent ticks repeat a time-of-day across days).
2. Both chart branches carry `ts` (epoch ms) on each rendered row
   (single-series: mapped points; multi-series: `ts` added to each
   `CombinedRow` at render). `XAxis` becomes `dataKey="ts" type="number"
   scale="time" domain={[min, max]}` matching dash0's reference; `pickTicks` /
   `buildTickLabels` / `formatTick` are replaced by the new module.
   `buildCombinedRows` stays untouched (B.3) — `connectNulls={false}` still
   needs the slot merge; granularity is equalized server-side so the
   min-across-series rule keeps its meaning.
3. **Time-proportional availability strip (B.4).** Each cell's `flex` weight
   is its slot duration (time to the next cell's start; the last cell uses the
   previous gap), so a cell sits under the slice of the plot it describes.
   `isolatedDot` keeps working unchanged (index-based neighbour lookup on the
   row array).

### Tests

- Go (`service_test` / `recent_results_test.go` / new `response_time_window_test.go`):
  window bound (points older than `HistoryDays` never appear; rollup `Since`
  ceiling at `responseTimeRollupSpan`); one live + one retired region → one
  series; NULL-region 0 ms marker → no series; windowed trim budgets
  (≤ limit per region, evenly spaced, newest+oldest kept); zero-window parity
  with the legacy trim.
- status0 unit (`bun test`): `chart-axis.test.ts` (domain/ticks over disjoint
  series) + `response-time-rollup.test.ts` additions: gap renders as a gap
  (no bridging rows), isolated sample still isolated under the numeric axis.
- Playwright (`web/status0/e2e/response-time-chart.spec.ts`): retired region +
  live one → exactly one legend entry; axis labels monotonic in time.
