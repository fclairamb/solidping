# Speed up the check detail page graph

## Context

The check detail page at `/dash0/orgs/$org/checks/$checkUid` (route file
`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`) feels slow
because of the `ResponseTimeChart` card. Two problems compound:

1. **The chart fetches far too much data.**
   `web/dash0/src/components/checks/response-time-chart.tsx:277-284` calls
   `useAllResults`, which pages through *every* result in the time window
   (`web/dash0/src/api/hooks.ts:662-708` — follows cursors until exhausted,
   `size: 1000` per page). For "month" view it asks for
   `raw,hour,day,month` tiers — on a 1-minute check that is ~30 000 rows.
   For "week" with `raw,hour,day` it's still ~10 000.
2. **Recharts then renders every point as a real SVG node.**
   The custom `dot={(props) => …}` renderer at
   `response-time-chart.tsx:587-641` creates a `<circle>` + `<title>`
   per point. With 1k+ points this stalls initial render and re-fires on
   every refetch.

Polling makes this worse: the page passes its `refetchInterval` (the
check's period — typically 60 000 ms; 1 500 ms during the first 30 s
after page load when the first result is still `created` —
`checks.$checkUid.index.tsx:296-306, 598`) down into the chart, so the
whole transform + re-render fires on that cadence.

`AvailabilityTable` (`web/dash0/src/components/checks/availability-table.tsx:134-153`)
runs a *second* `useAllResults` over a full year on the same polling
cadence. That doubles the network and CPU bill for the page.

The aggregator already keeps rolled-up tiers (`hour`, `day`, `month`) —
we just aren't using them as the primary source for the chart.

## Goal

A graph that loads in well under a second on any range, never renders
more than a few hundred points, and refetches at a sane cadence — on
the same page, against the same backend, without changing the API.

A page that polls at most once every 30 s for the chart, and once a
minute for the availability table.

## Approach

Frontend-only changes — no new endpoints, no schema changes. Five small
fixes, ranked by impact.

### 1. Pick one tier per range and stop fetching `raw` for week/month

`response-time-chart.tsx:268-275` — today:

```ts
const periodType =
  timeRange === "month" ? "raw,hour,day,month"
  : timeRange === "week" ? "raw,hour,day"
  : timeRange === "day"  ? "raw,hour"
  : "raw";
```

Replace with a single tier per range so the chart never fetches more
than ~200 rows:

- `hour` → `raw`
- `day` → `raw` if the check's period ≥ 5 min (≤ 288 points), else `hour`
- `week` → `hour` (≤ 168 points)
- `month` → `day` (~30 points)

The check's `periodMs` is already computed on the parent page
(`checks.$checkUid.index.tsx:287-290` — `parsePeriodMs(check?.period)`);
pass it into `ResponseTimeChart` as a new prop and use it to pick raw vs
hour for the `day` range.

The existing comment at `response-time-chart.tsx:263-267` warns that
querying a single tier "misses the rest of the timeline". That's only
true at the boundary where the rollup hasn't run yet (the trailing
minutes of an hour bucket, etc.). Accept a small visual gap at the
right edge — much better than rendering the whole timeline at raw
resolution.

### 2. Replace `useAllResults` with single-page `useResults` + a hard cap

`response-time-chart.tsx:277-284`. `useAllResults` follows cursors until
exhausted — that's exactly what we don't want for a chart. Switch to
`useResults` (single page, `api/hooks.ts:607-647`) with `size: 500` as
a defensive cap. With (1) the queries will normally stay well under
that, but the cap protects against any unexpected density.

### 3. Disable per-point `dot` rendering when data is dense; drop animation

`response-time-chart.tsx:587-676`.

- Set `dot` to `false` when `chartData.length > 150`. Keep `activeDot`
  so hover and click-to-pin still work — `activeDot` only renders one
  element.
- Set `isAnimationActive={false}` on the `<Area>`. With many points the
  300 ms transition costs more than it adds and re-fires on every poll.

The custom `dot` is what populates `dotPositions.current` for the
`PinnedResultBox` anchor. When dots are off, derive the anchor from
the `activeDot` callback at click time instead — `PinnedResultBox`
already takes a `{cx, cy}` anchor (`response-time-chart.tsx:679-689`),
so the change is local.

### 4. Floor the chart's `refetchInterval`

`checks.$checkUid.index.tsx:305-306, 598`. Today the chart polls at the
page's cadence: 1 500 ms for the first 30 s on a freshly-created check,
otherwise the check's period (often 60 000 ms). Neither is needed for
the *graph* — the user just wants to see the line move within a minute
or two.

Pass a chart-specific interval to `<ResponseTimeChart>`:

```ts
const chartRefetchInterval = Math.max(periodMs, 30_000);
```

…and don't honor the fast-poll window for the chart. The summary cards
/ `useCheck` / small `useResults(size:10)` can keep their current
cadence — they're cheap.

For `week`/`month` ranges, floor higher
(`Math.max(periodMs, 5 * 60_000)`) — 30-day rollups don't change in
30 seconds.

### 5. Stop `AvailabilityTable` from pulling raw rows

`availability-table.tsx:134-153`. Drop `rawResults` and the
`rawAvailability` fallback (`availability-table.tsx:151, 177-198, 230-236`).
The table only displays aggregated stats — it should fetch only
`periodType=hour,day,month` over a year, with `availabilityPct`. That's
~70 rows. Today it pulls a year of *raw* rows alongside, which on a
busy check is the single biggest network hit on the page.

Also bump its `refetchInterval` floor to 60 s — annual availability does
not move that fast.

If a brand-new check has no `hour` aggregation yet (rollup runs at the
end of each hour), today's row will read `-` for the first hour. That's
a fair tradeoff. If we want to avoid it, fetch the small recent raw
window separately with a tiny `size` cap (e.g. `size: 200`, last hour
only) — but ship without it first and add only if it's noticeably ugly.

## Files to change

- `web/dash0/src/components/checks/response-time-chart.tsx` — tier
  selection (1), single-page fetch (2), `dot`/animation (3), accept
  `periodMs` prop
- `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx` —
  chart-specific `refetchInterval` (4), pass `periodMs` through
- `web/dash0/src/components/checks/availability-table.tsx` — drop
  raw-row fetch + fallback (5), raise polling floor

## Things deliberately not done

- **No new server endpoint.** A `/checks/$uid/timeseries?resolution=auto`
  endpoint that pre-buckets points would be the textbook fix, but the
  existing tier system already gives us pre-aggregated buckets — we
  just weren't picking the right one. Skip until proven necessary.
- **No virtualization or canvas chart library.** With ≤ 200 points
  Recharts is fine; switching renderers is unnecessary churn.
- **Not touching `gradientStops` / `detectGaps` / `insertGapMarkers`.**
  They are O(n) over chartData; with n ≈ 100 they cost nothing. The
  volume problem dominates.

## Verification

End-to-end:

1. `make dev-test` (rebuilds backend + dash0 on file change).
2. Pick a busy check (1-minute period, plenty of history).
3. Open DevTools → Network. Switch through `hour` / `day` / `week` /
   `month`. For each, the response to `/api/v1/orgs/default/results`
   should return well under 500 rows (hour: ≤ 60, day: ≤ 288 or 24,
   week: ≤ 168, month: ~30).
4. Open DevTools → Performance, record a 5-second profile while sitting
   on the page. Refetch should not stall the main thread for more than
   a few ms (was previously seconds for `month`).
5. Confirm `dot` rendering is off on dense ranges (no per-point circles
   in the SVG) but `activeDot` still appears on hover and clicking still
   pins the result box.
6. Confirm `AvailabilityTable` loads the same numbers as before (compare
   a known check before/after).
7. Spot-check `web/dash0/e2e/` for any chart-related Playwright tests;
   if any rely on per-point `<circle>` selectors, update them.

No backend tests should be affected — this is a pure client change.

## Implementation Plan

1. **Tier selection by range**
   (`web/dash0/src/components/checks/response-time-chart.tsx`):
   accept a new `periodMs` prop and replace the comma-list `periodType`
   with one tier per range:
   - `hour` → `raw`
   - `day` → `raw` if `periodMs ≥ 5min`, else `hour`
   - `week` → `hour`
   - `month` → `day`
2. **Single-page `useResults` with size cap** (~500). Drop
   `useAllResults` from the chart.
3. **Drop dense-data dot rendering and animation**. `dot={false}` once
   `chartData.length > 150`; keep `activeDot`. Adapt the
   pinned-result-anchor logic to read `cx/cy` from the `activeDot`
   callback instead of the now-skipped `dot` writer.
4. **Floor the chart's `refetchInterval`** in
   `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`: pass a
   chart-specific interval, ignoring the fast-poll first-30s window.
   `Math.max(periodMs, 30_000)` for hour/day; `Math.max(periodMs, 5min)`
   for week/month.
5. **AvailabilityTable: drop the raw-row fetch + fallback** in
   `web/dash0/src/components/checks/availability-table.tsx`. Fetch only
   `periodType=hour,day,month` over a year. Bump `refetchInterval` floor
   to 60s.
6. **QA**: `make build-client lint-back` (frontend-only change; backend
   tests untouched).
7. **Audit + archive**.
