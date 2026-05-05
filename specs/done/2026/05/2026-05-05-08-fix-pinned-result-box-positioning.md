# Fix pinned result box positioning on response-time chart

## Context

On a check detail page, clicking a data point in the **Response Times** chart
(`web/dash0/src/components/checks/response-time-chart.tsx`) is supposed to
"pin" a small details popover anchored to the clicked dot
(`web/dash0/src/components/checks/pinned-result-box.tsx`). A second click on
the same dot navigates to the full result detail page.

In practice the popover frequently lands in the wrong place:

- Most often it ends up clipped against the **left edge** of the chart, with
  only ~50 px peeking through (X close button, fragmented labels).
- Sometimes it lands on top of the clicked dot itself, occluding the data the
  user is trying to read.

The behaviour is independent of which dot is clicked — clicking a point on
the right side of the chart still produces a box clipped on the left.

## Diagnosis

Two coupled bugs.

### 1. `chartWidth` is never measured (ResizeObserver setup race)

`response-time-chart.tsx` measures the wrapper width to clamp the popover
inside the visible area:

```tsx
const chartWrapperRef = useRef<HTMLDivElement | null>(null);
const [chartWidth, setChartWidth] = useState(0);

useEffect(() => {
  if (!chartWrapperRef.current) return;
  const observer = new ResizeObserver((entries) => { … setChartWidth(...) });
  observer.observe(chartWrapperRef.current);
  return () => observer.disconnect();
}, []);
```

The wrapper `<div ref={chartWrapperRef}>` is rendered **conditionally**, only
when `!isLoading && chartData.length > 0`. The effect runs once on mount with
an empty dep array. When the chart is still loading on mount (the common
case — first paint shows the `<Skeleton>`), `chartWrapperRef.current` is
`null`, the effect bails out, and the observer is **never re-attached** when
the wrapper later mounts. `chartWidth` stays at `0`.

### 2. Positioning math underflows when width is 0

`pinned-result-box.tsx` computes:

```tsx
leftPx = Math.min(Math.max(anchor.cx - half, MARGIN), width - BOX_WIDTH - MARGIN);
topPx  = Math.max(anchor.cy - 8 - 140, MARGIN);
```

With `width = 0`, `BOX_WIDTH = 240`, `MARGIN = 8`, the upper bound of the
clamp becomes `-248`, so `Math.min` returns `-248` regardless of `anchor.cx`.
The popover is pushed almost entirely off the left edge — exactly what the
screenshot shows.

The `topPx` formula additionally hard-codes the popover height at 140 px,
but the actual height varies with what the API returns (min/max, region,
availability, period type — each row can be present or absent). When the
dot is high in the chart, `topPx` falls back to `MARGIN` and the box lands
on top of the dot.

## Scope

In scope:

1. **Fix `chartWidth` measurement** so the popover always knows the chart's
   actual width.
2. **Fix popover positioning** so it:
   - never goes off-screen even if width measurement is missing,
   - never overlaps the anchor dot — flips above/below the dot based on
     available space,
   - handles the right edge of the chart (mirror to the left of the dot)
     symmetrically with the left edge.
3. **Manual smoke test** by clicking dots near each edge (top, bottom, left,
   right) of the chart in `make dev-test`.

Out of scope:

- Changing the popover's *content* or its API (`useResult` hook).
- Reworking the chart's data pipeline, gap detection, or aggregation tier
  selection.
- Touching the public status page chart (`web/status0`) — it does not have a
  pinned-details popover.

## Implementation

### `response-time-chart.tsx`

Replace the `useRef + useEffect` pair with a **callback ref** so the
`ResizeObserver` is attached the moment the wrapper enters the DOM, even on
later mounts (after the loading skeleton clears, or after switching between
Hour / Day / Week / Month tabs forces a re-render):

```tsx
const observerRef = useRef<ResizeObserver | null>(null);
const chartWrapperRef = useCallback((node: HTMLDivElement | null) => {
  observerRef.current?.disconnect();
  observerRef.current = null;
  if (!node) return;
  setChartWidth(node.getBoundingClientRect().width);
  const observer = new ResizeObserver((entries) => {
    for (const entry of entries) setChartWidth(entry.contentRect.width);
  });
  observer.observe(node);
  observerRef.current = observer;
}, []);
```

This sets `chartWidth` synchronously to the initial measurement (so the very
first click after the chart appears positions correctly) and re-installs the
observer on any later remount.

### `pinned-result-box.tsx`

Three changes:

1. **Clamp `leftPx` so the lower bound wins when the upper bound is smaller**
   (i.e. when `width` is 0 or smaller than the box). Use:

   ```tsx
   const upper = Math.max(width - BOX_WIDTH - MARGIN, MARGIN);
   leftPx = Math.max(MARGIN, Math.min(anchor.cx - BOX_WIDTH / 2, upper));
   ```

   Now `leftPx >= MARGIN` is invariant; with width=0 the box just lands at
   the left margin instead of off-screen.

2. **Measure the popover's actual height** with a ref + `useLayoutEffect`
   instead of hard-coding 140 px:

   ```tsx
   const boxRef = useRef<HTMLDivElement | null>(null);
   const [boxHeight, setBoxHeight] = useState(140);
   useLayoutEffect(() => {
     if (boxRef.current) setBoxHeight(boxRef.current.offsetHeight);
   });
   ```

3. **Place above by default, fall back to below** when there isn't room
   above the dot:

   ```tsx
   const GAP = 12; // distance between dot and box edge
   const above = anchor.cy - GAP - boxHeight;
   topPx = above >= MARGIN ? above : anchor.cy + GAP;
   ```

   This eliminates the "box on top of dot" failure mode without depending
   on knowing the chart's height.

Attach `boxRef` to the popover root `<div>` so the layout effect can measure
it.

## Test plan

`make dev-test`, log in, open a check detail page with a populated chart.

1. Click a dot near the **right edge** of the chart → popover lands to the
   left of the dot, fully on-screen, dot still visible.
2. Click a dot near the **left edge** → popover lands to the right of the
   dot, fully on-screen.
3. Click a dot near the **top** of the chart (a slow response peak) →
   popover appears **below** the dot, not overlapping it.
4. Click a dot near the **bottom** → popover appears above as usual.
5. Click the same dot twice → second click navigates to the result detail
   page (existing behaviour preserved).
6. Reload the page on a slow connection (or add a small delay) so the
   loading skeleton paints first → after the chart appears, the very first
   click positions correctly (regression test for the ResizeObserver race).
7. Switch between Hour / Day / Week / Month tabs and click a dot in each →
   positioning correct in all four (no stale measurement).

## Risks

- Switching `chartWrapperRef` to a callback ref changes its identity on
  every render. We avoid that by wrapping it in `useCallback` with `[]`
  deps, which keeps it stable.
- `useLayoutEffect` re-runs on every render; that's fine because it's a
  cheap measurement and the popover only renders while pinned.

## References

- `web/dash0/src/components/checks/response-time-chart.tsx`
- `web/dash0/src/components/checks/pinned-result-box.tsx`
- Screenshot from 2026-05-05 conversation showing the clipping.
