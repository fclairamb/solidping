# Badge Uptime Bar: Last Segment Renders Too Wide

## Context

The `status,uptime-bar` badge renders an availability strip split into `n`
equal-width segments. For `period=24h` that is `n = 24` hourly segments at the
default badge width of `300 px`.

On a 24h badge the **last (rightmost / current-hour) segment is rendered roughly
twice as wide as every other segment**, so the hourly bars are not evenly split.
The defect is glaringly visible on a fresh check, where the current hour is the
only colored (green) segment — it appears as a wide green block stuck to the
right edge, out of proportion with the 23 grey hour bars to its left.

This regressed in visibility (not in cause) after commit `c41c5eae`
("render current in-progress bucket in badge uptime-bar/graph"), which moved
`bucketStart` from `-(n)·dur` to `-(n-1)·dur` so the current in-progress hour is
now the **last** rendered segment and is populated. The width bug had always
existed but was previously hidden in the trailing (empty) segment.

## Root cause

`renderUptimeBarRow` in `server/internal/handlers/badges/svg.go` (lines ~178-191)
computes segment widths with integer floor division and dumps **all** of the
accumulated truncation remainder onto the single last segment:

```go
gaps := n - 1
availableWidth := width - gaps
segWidth := availableWidth / n                    // integer FLOOR division
lastSegWidth := availableWidth - segWidth*(n-1)   // last segment absorbs ALL remainder
...
if idx == n-1 { rectWidth = lastSegWidth }
```

For `period=24h` (`n = 24`, `width = 300`):

| value | result |
|---|---|
| `availableWidth` | `300 − 23 = 277` |
| `segWidth` (floor) | `277 / 24 = 11 px` |
| `lastSegWidth` | `277 − 11×23 = 24 px` |

So 23 segments render at **11 px** and the last at **24 px** — more than double.
The ~13 px of floor-division remainder, instead of being spread across the bar,
all lands on one segment.

This is purely a width-*distribution* bug. The data-window bug that `c41c5eae`
fixed is unrelated and remains fixed.

## Goal

Distribute the rounding remainder evenly across all segments so that rendered
segment widths differ by at most 1 px, regardless of period or badge width. No
single segment (least of all the current-hour segment) may be visibly wider than
the rest.

## Out of scope

- **Response-time graph row** — unaffected; it plots continuous points at
  proportional x-positions, not discrete equal-width rects.
- **Hour tick labels** (`hourTickLabels`, `service.go`) — they label segment
  indices `0/6/12/18` rather than wall-clock hours. That is a separate concern
  and is not addressed here.

## Changes required

### `server/internal/handlers/badges/svg.go` — `renderUptimeBarRow`

Replace the `segWidth` / `lastSegWidth` scheme with per-segment boundaries
(largest-remainder / proportional-boundary method). Each segment spans
`floor((idx+1)·availableWidth/n) − floor(idx·availableWidth/n)`:

```go
gaps := n - 1
availableWidth := width - gaps
...
posX := 0
for idx, color := range segments {
    // Even distribution: each segment spans
    //   floor((idx+1)*availableWidth/n) - floor(idx*availableWidth/n)
    // so widths differ by at most 1px and the rounding remainder is spread
    // across the bar instead of piling onto the last segment.
    rectWidth := (idx+1)*availableWidth/n - idx*availableWidth/n

    // ... existing rect + overlay + label rendering, using rectWidth ...

    posX += rectWidth + 1
}
```

Remove the now-unused `segWidth` / `lastSegWidth` locals and the
`if idx == n-1 { rectWidth = lastSegWidth }` special case. Update the comment at
line ~178 ("last segment gets remaining pixels") to describe even distribution.

This preserves the existing invariants:
- segment widths + `(n-1)` 1-px gaps sum to exactly `width`;
- the last segment's right edge still lands on `width`.

For `n = 24` / `width = 300`, every segment becomes 11 or 12 px (uniform). For
the other periods (`n = 2/3/7/30/90`) the produced widths are **identical** to
today (e.g. n=3/300 → 99,99,100 both before and after), so existing behavior and
tests are unchanged.

### `server/internal/handlers/badges/service.go` — `computeUptimeBarValues`

Comment-only change. The overlay min-width gate
(`segWidth = (width - (n - 1)) / n`, ~line 417) still uses the floor as the
minimum segment width and stays correct. Update the doc comment block
(~lines 403-407) that claims "the last segment absorbs the remainder and is
therefore always >= segWidth" — after the fix the remainder is spread evenly, so
reword to "the floor is the minimum segment width; widths differ by at most 1px".

## Tests

### `server/internal/handlers/badges/service_test.go`

- **New regression test** `TestRenderUptimeBarRowEvenWidths`: render
  `renderUptimeBarRow` with `n = 24`, `width = 300`; parse every `width="…"` from
  the `<rect>` elements and assert `max(width) − min(width) <= 1` (no segment
  more than 1 px wider than any other). Fails on current code (24 vs 11), passes
  after the fix. Optionally also assert the widths plus `(n-1)` gaps sum to 300.
- Confirm existing `TestRenderUptimeBarRowOverlay` / `…Labels` / `…NoLabels`
  (n=2,3) still pass unchanged.

### `server/test/integration/badges_test.go` (optional)

Extend an existing 24h-badge assertion to check that the rendered segment widths
are uniform (max − min ≤ 1).

## Acceptance criteria

1. `GET /api/v1/orgs/{org}/checks/{check}/badges/status,uptime-bar?period=24h`
   renders 24 segments whose widths are all within 1 px of each other (no 24 px
   segment).
2. The current-hour (rightmost) segment is the same width as the other hourly
   segments — no wide green block at the right edge.
3. Other periods (`7d`, `30d`, `90d`) render exactly as before.
4. All existing badge tests pass; the new regression test passes.

## Implementation Plan

1. **`renderUptimeBarRow`** (`server/internal/handlers/badges/svg.go`): swap the
   floor-division + last-segment-remainder math for the per-segment boundary
   formula; drop the `idx == n-1` special case; update the comment.
2. **`computeUptimeBarValues`** (`server/internal/handlers/badges/service.go`):
   update the doc comment only (no logic change).
3. **Tests** (`server/internal/handlers/badges/service_test.go`): add
   `TestRenderUptimeBarRowEvenWidths`; verify the existing n=2/3 tests still pass.
4. **QA**: `make build && make lint && make test`; fix until green. Then fetch
   the live badge and confirm via the SVG that all 24 `<rect>` widths are 11–12 px:
   `curl -s 'http://localhost:4000/api/v1/orgs/default/checks/http-cloudflare-dns-2/badges/status,uptime-bar?period=24h'`.
