# Badge contextual labels: uptime-bar weekday ticks + response-time-graph grid

## Problem

The `uptime-bar` and `response-time-graph` badge rows carry no temporal or
value reference, making them hard to interpret in isolation:

- **Uptime bar** — the coloured segments convey trend but no time anchoring.
  A viewer cannot tell which segment is "today" or "last Monday" without
  external context.
- **Response-time graph** — the Y axis is unlabelled.  The line's shape shows
  relative variation but gives no idea of the actual response-time scale.

Reproduce at:
`/dash0/orgs/default/badges?check=sftp-test-rebex&components=status,availability,duration,response-time,uptime-bar,response-time-graph&period=7d`

## Changes

### 1 — Uptime bar: per-segment time labels

Add a narrow label band (10 px) below the coloured bar so each badge period
gets appropriate tick marks:

| Period | Segments | Labels shown |
|--------|----------|--------------|
| `7d`   | 7 (1/day)   | 3-letter weekday for **every** segment: Mon, Tue, … |
| `24h`  | 24 (1/hour) | Hour mark every 6 h: 0h, 6h, 12h, 18h |
| `30d`  | 30 (1/day)  | "Jan 2" at every Monday + first segment |
| `90d`  | 90 (1/day)  | 3-letter month at the first day of each month + first segment |

Labels that fall within 20 px of one another after accounting for segment
width may be omitted to avoid overlap (use the spacing rules above to ensure
this never happens in practice).

Row height grows from `rowHeightBar = 20` to `rowHeightBar = 30` to
accommodate the text band.  The 10 px added is always present (even when all
labels are empty strings) so the row height is stable across periods.

Label style: 7 px, `fill="#777"`, `font-family` same as other badge text,
`text-anchor="middle"`, centred under each labelled segment.

### 2 — Response-time graph: Y-axis grid

Add 2 horizontal gridlines inside the graph area so the viewer can read
approximate values off the line:

- **Top gridline**: at the actual (unpadded) maximum response-time value in
  the data set, positioned at `yAt(actualMax)`.
- **Bottom gridline**: at the actual (unpadded) minimum, at `yAt(actualMin)`.
- When `actualMin == actualMax` (flat or single-point series): only 1
  gridline at that value.

Visual spec:
```
stroke="#ccc"  stroke-width="0.5"  stroke-dasharray="2,2"
```

Value label on the **right edge** of each gridline:
- `x = width - 2`
- `y = yAt(value) - 1`  (baseline sits 1 px above the gridline)
- 7 px, `fill="#888"`, `text-anchor="end"`
- Format: same logic as `formatResponseTime` — ms below 1 000 ms, one
  decimal second above (e.g. `304ms`, `1.2s`).

Grid elements are inserted into the `<g>` **before** the area fill and
polyline so they render behind the chart data.

No height change for the graph row (`rowHeightGraph = 40` is sufficient for
7 px labels).

## Scope

All changes are in `server/internal/handlers/badges/`:

- **`svg.go`**
  - `renderUptimeBarRow` gains a `labels []string` parameter (one entry per
    segment; empty string = no label for that slot).  The function renders the
    coloured bar in the top 20 px and the label text in the bottom 10 px of
    `height`.
  - `rowHeightBar` constant: `20 → 30`.
  - `renderResponseTimeGraphRow` gains grid logic (gridlines + labels) drawn
    before the area/line fragments.  The function already has `minV`/`maxV`
    from `paddedRange`; retrieve the unpadded range via a new internal call to
    `pointsRange` (already exported for tests) before padding.
  - Add `formatDurationMs(ms float64) string` — returns `"Xms"` or `"X.Xs"`
    (extracted from the grid-label rendering so it can be unit-tested).

- **`service.go`**
  - `appendRowFragments` computes `labels []string` from `bucketStart`,
    `bucketDuration`, and `n` (a pure function, no DB access), then passes
    them to `renderUptimeBarRow`.
  - Add `computeUptimeBarLabels(bucketStart time.Time, n int, bucketDuration time.Duration) []string`.

No frontend, API, or DB changes.

## Acceptance criteria

- At `?period=7d` the uptime-bar shows Mon…Sun below the coloured segments.
- At `?period=24h` the uptime-bar shows 0h, 6h, 12h, 18h tick marks.
- At `?period=30d` the uptime-bar shows "Jan 2"-style labels at week boundaries.
- At `?period=90d` the uptime-bar shows month-name labels at month boundaries.
- The response-time graph renders 1–2 dashed horizontal gridlines with value
  labels on the right edge whenever there is data.
- When the graph has no data the empty grey frame is unchanged (no gridlines).
- `make test` passes including the existing badge unit tests.
- `make lint` passes without relaxing any rule.

## Implementation plan

### Step 1 — `computeUptimeBarLabels` in `service.go`

Add the pure helper:

```go
func computeUptimeBarLabels(bucketStart time.Time, n int, bucketDuration time.Duration) []string {
    labels := make([]string, n)
    switch {
    case bucketDuration == 24*time.Hour && n == 7:
        for i := range n {
            labels[i] = bucketStart.Add(time.Duration(i) * bucketDuration).Weekday().String()[:3]
        }
    case bucketDuration == time.Hour && n == 24:
        for i := range n {
            if i%6 == 0 {
                labels[i] = fmt.Sprintf("%dh", i)
            }
        }
    case bucketDuration == 24*time.Hour && n == 30:
        for i := range n {
            t := bucketStart.Add(time.Duration(i) * bucketDuration)
            if i == 0 || t.Weekday() == time.Monday {
                labels[i] = t.Format("Jan 2")
            }
        }
    case bucketDuration == 24*time.Hour && n == 90:
        for i := range n {
            t := bucketStart.Add(time.Duration(i) * bucketDuration)
            if i == 0 || t.Day() == 1 {
                labels[i] = t.Format("Jan")
            }
        }
    }
    return labels
}
```

Call it in `appendRowFragments` just before `renderUptimeBarRow`.

### Step 2 — Update `renderUptimeBarRow` in `svg.go`

- Change signature: `func renderUptimeBarRow(segments []string, labels []string, width, height, yOffset int, style string) string`
- Keep the coloured-rect loop unchanged (uses top 20 px of `height`).
- After the `</g>` that clips the coloured rects, append a second loop that
  emits `<text>` for each non-empty `labels[i]`, centred under that segment
  at `y = yOffset + 28` (20 px bar + 8 px baseline).
- Update `rowHeightBar = 30`.
- Update `GenerateUptimeBarSVG` (test wrapper) to pass an empty `labels`
  slice and `height = rowHeightBar`.

### Step 3 — Add grid to `renderResponseTimeGraphRow` in `svg.go`

- Before calling `paddedRange`, call `pointsRange` a second time to capture
  `actualMin, actualMax, hasData`.
- Build grid lines:
  ```go
  gridVals := []float64{actualMax}
  if actualMin != actualMax {
      gridVals = append(gridVals, actualMin)
  }
  ```
- For each `gv` in `gridVals`, emit a `<line>` and a `<text>` into a
  `grid` builder; insert `grid.String()` into the returned fragment
  before `areas`, `lines`, and `dots`.
- Add `formatDurationMs`:
  ```go
  func formatDurationMs(ms float64) string {
      if ms < 1000 {
          return fmt.Sprintf("%dms", int(math.Round(ms)))
      }
      return fmt.Sprintf("%.1fs", ms/1000)
  }
  ```

### Step 4 — Unit tests in `service_test.go`

- `TestComputeUptimeBarLabels_7d`: assert `labels[0]` matches weekday of
  `bucketStart`, all 7 labels are non-empty, values are 3 chars.
- `TestComputeUptimeBarLabels_24h`: assert labels at indices 0, 6, 12, 18
  are `"0h"`, `"6h"`, `"12h"`, `"18h"` and all others are `""`.
- `TestFormatDurationMs`: assert `304.0 → "304ms"`, `1200.0 → "1.2s"`.

### Step 5 — Verify

```bash
make test
make lint
```

Visual check:
```
curl -s 'http://localhost:4000/api/v1/orgs/default/checks/sftp-test-rebex/badges/uptime-bar,response-time-graph?period=7d' | grep -E '(text|line)'
```
- Expect `<text>` elements with Mon…Sun below the bar.
- Expect `<line>` elements inside the graph `<g>`.

## Implementation Plan

This consolidates the detailed steps above into the concrete, ordered work items
implemented on `feat/badge-uptime-labels-graph-grid`:

1. **`service.go` — `computeUptimeBarLabels`**: add the pure helper
   `computeUptimeBarLabels(bucketStart time.Time, n int, bucketDuration time.Duration) []string`
   per the period table (7d weekdays, 24h 6-hour ticks, 30d week boundaries,
   90d month boundaries). Call it in `appendRowFragments` and pass the result to
   `renderUptimeBarRow`.
2. **`svg.go` — `renderUptimeBarRow`**: add a `labels []string` parameter; render
   the coloured bar in the top 20 px and per-segment centred labels in the bottom
   10 px (7 px, `fill="#777"`, `text-anchor="middle"`, baseline at `yOffset+28`).
   Bump `rowHeightBar` from `20` to `30`. Update `GenerateUptimeBarSVG` to pass an
   empty labels slice.
3. **`svg.go` — `renderResponseTimeGraphRow` grid + `formatDurationMs`**: capture
   the unpadded `actualMin/actualMax` via `pointsRange` before padding; emit 1–2
   dashed gridlines (`stroke="#ccc" stroke-width="0.5" stroke-dasharray="2,2"`)
   with right-edge value labels (`x=width-2`, `y=yAt(v)-1`, 7 px, `fill="#888"`,
   `text-anchor="end"`) inserted **before** the area/line/dot fragments. Add
   `formatDurationMs(ms float64) string` (`"Xms"` / `"X.Xs"`). No grid when there
   is no data. No graph-row height change.
4. **Tests** in `service_test.go`: `TestComputeUptimeBarLabels_7d`,
   `TestComputeUptimeBarLabels_24h`, `TestComputeUptimeBarLabels_30d`,
   `TestComputeUptimeBarLabels_90d`, `TestFormatDurationMs`, plus grid-rendering
   assertions in `TestRenderResponseTimeGraphRow` and label assertions for the
   updated `renderUptimeBarRow`. Update existing `GenerateUptimeBarSVG` height
   expectations.
5. **QA**: `make build-backend build-client lint-back test`; visual sanity check
   via the badge endpoint.
