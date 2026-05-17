# Fill the current-bucket gap on check-detail graph and availability table

## Context

The check detail page at `/dash0/orgs/$org/checks/$uid?graphPeriod=week`
shows two broken surfaces when the check is young (< 24 h) or when the
selected range includes the most-recent day:

1. **Chart (Week / Month)** — renders a tiny cluster of hour-tier points
   instead of a continuous line spanning the selected window. The x-axis
   labels (`HH:mm`) make a 7-day domain look like a single day because
   `adaptiveFormat` (`response-time-chart.tsx:65-74`) formats based on
   `actualDataSpan` — the span of the data returned, not the requested
   window.

2. **Availability table** — all four rows ("Today", "Last 7 days",
   "Last 30 days", "Last 365 days") show `-` for Availability and
   Downtime, and `0` Incidents, even though raw check data clearly
   exists (the graph cluster above proves it).

### Root cause: the aggregator's structural blind spot

The aggregator (`server/internal/jobs/jobtypes/job_aggregation.go:270-291`,
`calculateAggregationBoundary`) keeps raw rows for the most recent
`retention_raw` hours (default: 24) before rolling them up to `hour`
and deleting the source rows. It also never emits a rollup for an open
(not-yet-closed) bucket.

This creates two permanent dead zones:

- **The rolling 24 h retention window** — any `hour`-only query over
  the past 7 days silently misses the most recent ~24 h. The chart's
  "week" view loses up to one day on the right edge.
- **The current open bucket** — `hour`, `day`, and `month` never get a
  row for the period that is currently in progress. So "today" never
  has a `day` row, and "this month" never has a `month` row, ever.

For a brand-new check (< 24 h of history) these two gaps cover the
entire window — the chart returns an empty result for `periodType=hour`
and the availability table returns empty results for all four
aggregated-only queries.

### Prior decision being amended

`specs/done/2026/05/2026-05-08-06-check-detail-graph-perf.md` (§1, §5)
deliberately chose:

- Chart: one tier per range (`week → hour`, `month → day`) to minimise
  payload.
- AvailabilityTable: aggregated tiers only (`periodType=hour,day,month`)
  and explicitly accepted that "today's row will read `-` for the
  first hour" as a fair tradeoff.

Reality has shown that "first hour" underestimates the problem:
the leading edge gap is permanent (open bucket), not just one hour.
This spec amends §1 and §5 of the earlier spec by adding **raw as a
co-tier** — not reverting to the pre-perf approach (no unbounded raw
fetches, no pagination explosions).

### Secondary issue: `useResults` `limit=500` truncates the left edge

`response-time-chart.tsx:293-300` calls `useResults` with `size: 500`.
The backend query orders `period_start DESC` and applies `LIMIT`
server-side (`server/internal/db/sqlite/sqlite.go:1614-1683`,
`server/internal/handlers/results/handler.go:85`). For multi-region
checks at `week` range, `N_regions × 168 hour_rows` can exceed 500 —
the server returns only the most-recent 500 rows, silently dropping the
left edge of the chart.

## Goal

A check detail page where:

- The **Week** graph shows a continuous line spanning 7 days from the
  very first raw result of a new check — no 24 h hole on the right
  edge, no missing left edge for multi-region checks.
- **Month** behaves the same over 30 days.
- The **Availability table** shows real numbers in all four rows as
  soon as any raw result exists, regardless of whether the aggregator
  has run.
- **Payload stays bounded**: raw retention (default 24 h) caps the
  additional cost — the table goes from ~70 aggregated rows to ~70 +
  up-to-1 440 raw rows; the chart's week view from ~168 to ~168 +
  ~1 440; month is similar.

## Approach

Five small changes. Frontend only — no new endpoints, no schema changes.

### 1. Chart — include `raw` as a co-tier for week/month

`web/dash0/src/components/checks/response-time-chart.tsx:272-282`

```ts
const denseEnoughForHourly = (periodMs ?? 60_000) < 5 * 60_000;
const periodType =
  timeRange === "month"
    ? "raw,hour,day"
    : timeRange === "week"
      ? "raw,hour"
      : timeRange === "day"
        ? denseEnoughForHourly
          ? "raw,hour"
          : "raw"
        : "raw";
```

The aggregator deletes source rows after rollup, so raw + hour + day
are disjoint in time — no duplicates. `detectGaps`
(`response-time-chart.tsx:94-135`) is already tier-aware and handles
raw↔hour↔day transitions correctly.

### 2. Chart — switch from `useResults` to `useAllResults`

`response-time-chart.tsx:293-300`

`useAllResults` (`web/dash0/src/api/hooks.ts:666-712`) follows cursors
until the window is exhausted. This eliminates the `limit=500`
left-edge truncation. Keep `size: 1000` as the per-page cap; raw
retention naturally bounds total row counts.

### 3. Availability table — request raw rows + the fields to compute from them

`web/dash0/src/components/checks/availability-table.tsx:139-146`

```ts
periodType: "raw,hour,day,month",
with: "availabilityPct,totalChecks,successfulChecks,status",
```

### 4. Availability table — bucket by timestamp (tier-agnostic)

`availability-table.tsx:149-158`

Drop the three per-tier filters (`hourlyResults`, `dailyResults`,
`monthlyResults`) and replace with four simple timestamp filters that
accept any tier:

```ts
const { todayRows, last7Rows, last30Rows, last365Rows } = useMemo(() => {
  const data = allResults?.data ?? [];
  const now = new Date();
  const todayStart = startOfDay(now).getTime();
  const day7   = subDays(now, 7).getTime();
  const day30  = subDays(now, 30).getTime();
  const day365 = subDays(now, 365).getTime();
  return {
    todayRows:   data.filter((r) => r.periodStart && new Date(r.periodStart).getTime() >= todayStart),
    last7Rows:   data.filter((r) => r.periodStart && new Date(r.periodStart).getTime() >= day7),
    last30Rows:  data.filter((r) => r.periodStart && new Date(r.periodStart).getTime() >= day30),
    last365Rows: data.filter((r) => r.periodStart && new Date(r.periodStart).getTime() >= day365),
  };
}, [allResults]);
```

### 5. Availability table — unified `computeAvailability`

`availability-table.tsx:171-180`

Replace `avgAvailability` (simple average of `availabilityPct`) with
`computeAvailability(rows)` that handles both raw and aggregated rows,
mirroring `calculateAvailability` in
`server/internal/handlers/badges/service.go:369-396`. Aggregated rows
are weighted by their real sample count:

```ts
function computeAvailability(data: OrgResult[] | undefined): number | null {
  if (!data?.length) return null;
  let successful = 0;
  let total = 0;
  for (const r of data) {
    if (r.periodType === "raw") {
      if (r.status === "created" || r.status === "running") continue;
      total += 1;
      if (r.status === "up") successful += 1;
    } else if (r.totalChecks != null && r.successfulChecks != null) {
      total += r.totalChecks;
      successful += r.successfulChecks;
    } else if (r.availabilityPct != null) {
      // fallback: treat bucket as one sample (cached / old response)
      total += 1;
      successful += r.availabilityPct / 100;
    }
  }
  return total === 0 ? null : (successful / total) * 100;
}
```

Import `OrgResult` from `@/api/hooks` alongside `IncidentDetail`.

### 6. Type — add `"running"` to `OrgResult.status`

`web/dash0/src/api/hooks.ts:111`

The server (`server/internal/handlers/results/service.go:334-351`)
serialises `ResultStatusRunning` as the string `"running"`. The current
`OrgResult` union does not include it:

```ts
// before
status?: "up" | "down" | "unknown" | "created";

// after
status?: "up" | "down" | "unknown" | "created" | "running";
```

Without this, `r.status === "running"` in `computeAvailability` does
not typecheck.

## Files to change

| File | Changes |
|---|---|
| `web/dash0/src/api/hooks.ts` | 6 — add `"running"` to `OrgResult.status` |
| `web/dash0/src/components/checks/response-time-chart.tsx` | 1 — tier selection; 2 — `useAllResults` |
| `web/dash0/src/components/checks/availability-table.tsx` | 3, 4, 5 — raw co-tier, unified bucket + calc |

## Things deliberately not done

- **No new server endpoint** (e.g. `/checks/$uid/stats`). The badges
  path shows the calculation can live anywhere; keeping it in the
  frontend avoids API surface churn and matches the prior spec's
  decisions.
- **No change to the aggregator.** Open buckets are conceptually
  incomplete; aggregating them mid-flight would require re-emitting
  every minute and break the "delete source rows" invariant.
- **No bump to `ParsePageLimit`** (`server/internal/handlers/results/handler.go:85`).
  `useAllResults` pagination handles multi-page results without needing
  a higher cap.
- **Raw is a co-tier, not a fallback.** Unlike the pre-`d99a4198`
  approach (try raw if aggregated is empty), raw rows are always
  requested alongside aggregated ones. `computeAvailability` weights
  them equally — the distinction only matters for raw's `totalChecks`
  being always 1.

## Verification

End-to-end:

1. `make dev-test` (per `web/CLAUDE.md` — backend + dash0 hot reload
   on file change).
2. Login at `http://localhost:4000/dash0/` with `test@test.com` /
   `test` / org `test` (RUNMODE=test).
3. Create a brand-new check with a 1-minute period. Within 2 minutes
   of the first run:
   - `?graphPeriod=week` — chart shows a continuous line ending at
     "now", not a small isolated cluster. X-axis labels reflect the
     full 7-day span (e.g. `EEE HH:mm` not `HH:mm`).
   - Switch to Month — same.
   - Toggle "Full range" off and on — both show data.
4. Availability table — within 2 minutes of the first raw result:
   - "Today" row shows a real `%` and `0s` downtime (not `-`).
   - All four rows have numbers.
5. Pick a check with > 30 days of history — verify table values match a
   manual calculation from `/api/v1/orgs/$org/results?checkUid=...`.
6. Multi-region check on Week view — confirm no left-edge truncation
   (previously lost when `N_regions × 168 > 500`).
7. Extend `web/dash0/e2e/` with a Playwright case that:
   - Creates a check via API,
   - Triggers one raw result (or waits for the first automated run),
   - Loads the check detail page,
   - Asserts the "Today" availability cell is not `-`.
8. `make lint` — no rule relaxations.
9. `cd web/dash0 && bun run build` — no type errors.

## Implementation plan

1. `web/dash0/src/api/hooks.ts` — add `"running"` to
   `OrgResult.status` union.
2. `web/dash0/src/components/checks/response-time-chart.tsx` —
   a. Swap `import { useResults }` for `import { useAllResults }`.
   b. Replace `periodType` selection with co-tier version (§1 above).
   c. Swap `useResults(...)` call for `useAllResults(...)` with
      `size: 1000`.
3. `web/dash0/src/components/checks/availability-table.tsx` —
   a. Import `OrgResult` from `@/api/hooks`.
   b. Change `periodType` to `"raw,hour,day,month"` and `with` to
      include `totalChecks,successfulChecks,status`.
   c. Replace three per-tier `useMemo` filters with four
      timestamp-bucketed filters.
   d. Replace `avgAvailability` with `computeAvailability` as defined
      in §5; update `useMemo` dependency array.
4. `make lint` + `cd web/dash0 && bun run build`.
5. Move spec to `specs/done/2026/05/` after merge.
