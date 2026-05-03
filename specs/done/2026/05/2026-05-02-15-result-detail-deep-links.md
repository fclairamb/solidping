# Result detail — deep links from chart and availability rows

## Context

Companion to `2026-05-02-07-result-detail-page-frontend.md` (which adds the result detail page) and its backend twin. Once the detail page exists, the natural follow-on is making every visual on the check detail page (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`) drill into one specific result.

A correction worth noting up front: I initially suggested making the **AvailabilityTable cells** clickable, but a re-read of `web/dash0/src/components/checks/availability-table.tsx` shows that table is **summary rows** (Today / Last 7 days / Last 30 days / Last 365 days), where each row's "Availability" cell is the *average* of many aggregation rows computed client-side. There's no 1:1 mapping from a cell to a single result UID — making cells link to a result detail would be either misleading or arbitrary.

The visual that *is* 1:1 with result rows is **`response-time-chart.tsx`** — every plotted point comes from one result row (`r.periodStart`, `r.durationMs`, `r.status`, `r.region` at lines 240–249). Each one has a UID. Those points are the right deep-link target.

This spec therefore does two distinct things:
1. **Chart points → result detail page** (true per-result deep link).
2. **AvailabilityTable rows → drill-down within the same page** (no per-result deep link; instead, scroll to and reconfigure the response-time chart so the user sees the data behind that row).

## Scope

In scope:
- Thread `uid` through `ChartPoint` and add a click handler that navigates to the result detail page.
- Hover affordance on chart points (cursor change + slightly enlarged dot) so users discover the link.
- Make `AvailabilityTable` rows clickable, scrolling to the response-time chart and switching its `graphPeriod` to the closest matching range.

Out of scope:
- Clickable area between points (only the dot itself navigates).
- Adding a separate filtered results-list page. That's a real product question (do we want a paginated results browser?) and bigger than this spec — defer to its own discussion.
- Multi-region disambiguation in the chart (each point is one region's result; if a check runs in N regions, the chart already plots N points per timestamp from the same series — clicking *any one* of them deep-links correctly to that specific row).

## Part 1 — Chart points become deep links

### `web/dash0/src/components/checks/response-time-chart.tsx`

Today the chart's `ChartPoint` (line 30) is:

```ts
interface ChartPoint {
  ts: number;
  durationMs: number | null;
  status: string;
}
```

Add `uid?: string`. Optional because gap-marker points (the `null`-duration spacers inserted by `insertGapMarkers`, lines 121–147, and the boundary markers in `fullRange` mode, lines 266–296) don't have a uid — those should NOT be navigable. Only real data points carry a uid.

In the `.map(...)` at line 242 that turns API rows into chart points, also pass `r.uid`:

```ts
return {
  ts: new Date(r.periodStart!).getTime(),
  durationMs: r.durationMs ?? 0,
  status: r.status ?? "up",
  uid: r.uid,
};
```

### Click handler

Recharts dot click is awkward — the recommended pattern is to render a custom `<Dot>` component via the `dot` prop on `<Area>`. The `Area` is around lines 400+ (find the `<Area dataKey="durationMs" ... />` in the same file). Replace the default dot with:

```tsx
import { useNavigate } from "@tanstack/react-router";
// ...
const navigate = useNavigate();

const ClickableDot = (props: {
  cx?: number; cy?: number; payload?: ChartPoint;
}) => {
  const { cx, cy, payload } = props;
  if (cx == null || cy == null || !payload?.uid) return null;
  const fill =
    payload.status === "up" ? "var(--chart-up, #22c55e)" :
    payload.status === "down" ? "var(--chart-down, #ef4444)" :
    "var(--chart-warn, #eab308)";
  return (
    <circle
      cx={cx} cy={cy} r={3.5}
      fill={fill}
      style={{ cursor: "pointer" }}
      onClick={() => navigate({
        to: "/orgs/$org/checks/$checkUid/results/$resultUid",
        params: { org, checkUid, resultUid: payload.uid! },
      })}
    >
      <title>Click for details</title>
    </circle>
  );
};

// then on <Area>:
<Area
  dataKey="durationMs"
  // ... existing props
  dot={<ClickableDot />}
  activeDot={{ r: 5, style: { cursor: "pointer" } }}
/>
```

Notes:
- `<title>` inside `<circle>` gives a native browser tooltip on hover — accessible and free. Don't add a custom React tooltip layer just for this.
- `r=3.5` matches recharts' default dot size; `activeDot r=5` gives the hover-grow affordance.
- The colour fallback values are placeholders — match whatever the existing chart already uses (grep in the same file for `fill=` or `stroke="var(`). If the chart currently relies on Tailwind classes like `[--chart-1:...]`, follow that convention instead of inline hex.
- `org` and `checkUid` come from the chart's props (`ResponseTimeChartProps` at line 21).

### Density consideration

In `month` time range with 30 days × hourly = ~720 points, every point becoming a clickable dot is fine — that's about the same as a status page heatmap. In `hour` mode with 1-min raw results = ~60 points, still fine. No need for a "show only N dots" optimization.

The recharts `dot` prop correctly skips rendering when `payload.durationMs == null` (gap markers), but defensively the `ClickableDot` returns `null` when `!payload?.uid` — belt and braces.

### Tooltip change (small)

`CustomTooltip` (find it in the same file, currently shows time + duration + status) should add a one-line hint: `t("checks:detail.chart.clickToView")` → "Click point for details" / "Cliquez pour les détails" / etc. Use the `checks` namespace. Don't show the hint on gap-marker / boundary-marker points (where there's no uid).

### `OrgResult` already has `uid`

Confirmed at `web/dash0/src/api/hooks.ts:101`. No type changes needed beyond `ChartPoint`.

## Part 2 — AvailabilityTable rows become drill-downs

### Honest scope statement (please don't skip)

These rows are NOT result deep links. Each row averages many aggregation rows. Making the row navigate to "one result" would be wrong. What the row *can* do is drive the response-time chart's `graphPeriod` so the user can immediately see the underlying data for that period.

Today the chart accepts `graphPeriod ∈ {"hour", "day", "week", "month"}` (Route's `validateSearch` in `checks.$checkUid.index.tsx:62`). The mapping is approximate:

| AvailabilityTable row | Closest chart graphPeriod | Honest note                               |
|-----------------------|---------------------------|-------------------------------------------|
| Today                 | `day` (Last 24h)          | "Today since 00:00" vs "Last 24h" — close |
| Last 7 days           | `week`                    | exact                                     |
| Last 30 days          | `month`                   | exact                                     |
| Last 365 days         | `month`                   | mismatch — no 365d range in chart         |

For the 365-day case, two options:
- **(a)** Disable the row click (no drill-down available).
- **(b)** Still navigate to `month` and let the user know via a toast or row-level hint.

Pick **(a)**. A click that doesn't fully land on the right view is worse than no click at all.

### Implementation

In `web/dash0/src/components/checks/availability-table.tsx`:

1. Add an optional `onPeriodSelect?: (period: "day" | "week" | "month") => void` prop.
2. Map row labels → chart period:
   ```ts
   const ROW_TO_GRAPH: Record<string, "day" | "week" | "month" | null> = {
     "Today": "day",
     "Last 7 days": "week",
     "Last 30 days": "month",
     "Last 365 days": null,
   };
   ```
3. Make rows with a non-null mapping clickable: `onClick={() => onPeriodSelect?.(ROW_TO_GRAPH[row.label]!)}`, plus `cursor-pointer hover:bg-muted/50` on the `<TableRow>`. Rows that map to `null` keep default styling and no click.

In `checks.$checkUid.index.tsx` at line 553 where `<AvailabilityTable>` is rendered, wire it up:

```tsx
const chartRef = useRef<HTMLDivElement>(null);

// Wrap <ResponseTimeChart> in <div ref={chartRef}>...</div>

<AvailabilityTable
  org={org}
  checkUid={checkUid}
  refetchInterval={refetchInterval}
  onPeriodSelect={(period) => {
    navigate({
      to: ".",
      search: { graphPeriod: period === "day" ? undefined : period, graphFull: undefined },
      replace: true,
    });
    // Wait one frame for the chart to re-render at the new period, then scroll.
    requestAnimationFrame(() => {
      chartRef.current?.scrollIntoView({ behavior: "smooth", block: "start" });
    });
  }}
/>
```

(The `period === "day"` → `undefined` mapping matches the existing default-state convention at line 543.)

### i18n

The label-to-period mapping uses English row labels because the labels are currently hard-coded English in `availability-table.tsx:30,35,40,45`. A separate translation pass for the AvailabilityTable already wants to land (the `Today` / `Last 7 days` strings are English-only); when that lands, switch the lookup key from `row.label` to a stable id (`row.id: "today" | "last7" | "last30" | "last365"`) so it survives translation. This spec adds the `id` field to make the future translation spec a clean follow-on.

## Verification

1. `make build-dash0` — TypeScript compiles cleanly.
2. `make dev-test` — running on `:4000`. Manual checks:
   - Navigate to a check detail page. Hover a chart point: cursor changes to pointer, native tooltip says "Click for details." Click → result detail page loads with the correct uid in the URL. Refresh the URL: still works.
   - Hover a gap-marker region (visible in `?graphFull=true` for a check with downtime): no clickable dot.
   - Click "Today" / "Last 7 days" / "Last 30 days" rows in the AvailabilityTable: URL updates with the new `graphPeriod`, chart re-renders, page scrolls to it. Click "Last 365 days": no navigation, no scroll.
   - In `month` graphPeriod (~720 hourly points), the page is still responsive — no observable lag from per-dot click handlers.
3. `make build` (full bundle, embeds dash0). Visit the bundled app on `:4000`, repeat smoke. Production builds sometimes optimize recharts' `dot` differently from dev — worth a 30-second sanity check.

## Risks worth knowing

- Recharts' custom `<Dot>` rendering used to have a regression where `dot` with a function/component disabled the line. If the line disappears, fall back to `dot={(props) => <ClickableDot {...props} />}` (function form) or to a wrapper that always renders the line and overlays clickable markers. Test in dev before declaring done.
- The chart point click vs Recharts hover/tooltip event ordering: clicking a point may fire the tooltip's mouseLeave first; `onClick` on the dot should still win. If not, the fallback is `onMouseDown`. Don't add a `setTimeout` workaround.

## Final grep check

```bash
rtk grep -rn "ClickableDot\|onPeriodSelect" web/dash0/src/   # 4–6 hits across chart + table + page
```
