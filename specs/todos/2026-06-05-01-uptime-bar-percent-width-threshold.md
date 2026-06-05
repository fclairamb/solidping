# Uptime Bar: Show Percentage Based on Segment Width Threshold

## Context

The uptime-bar component in badge SVGs can overlay a per-day availability percentage on each colored segment. Currently `computeUptimeBarValues` in `server/internal/handlers/badges/service.go` hard-codes this to the 7d period only (`bucketDuration == 24h && n == 7`), because that combination produces ~42px-wide segments at the default 300px badge width — wide enough to fit the text.

The 7d/300px assumption breaks when the caller requests a narrower badge (e.g. `?width=200`), and it also means the overlay could never appear on a wider badge with a shorter period if segments happened to be wide enough.

## Goal

Show the per-segment availability percentage inside each uptime-bar segment whenever that segment's rendered pixel width exceeds **30 px**, regardless of which period is active or what the badge width is. Suppress the overlay otherwise.

## Segment-width formula

`renderUptimeBarRow` divides the bar as follows:

```
availableWidth = width - (n - 1)   // 1-px gaps between n segments
segWidth       = availableWidth / n  // integer division; last seg gets the remainder
```

The overlay threshold applies to `segWidth` (the minimum segment width — the last segment is always ≥ this).

### Concrete cases

| Badge width | Period (n segments) | segWidth | Show % |
|-------------|---------------------|----------|--------|
| 300 px      | 7d (7 segs)         | (300−6)/7 = **42 px** | ✓ |
| 200 px      | 7d (7 segs)         | (200−6)/7 = **27 px** | ✗ |
| 300 px      | 30d (30 segs)       | (300−29)/30 = **9 px** | ✗ |
| 300 px      | 24h (24 segs)       | (300−23)/24 = **11 px** | ✗ |

The threshold of 30 px matches the current implicit behaviour: 7d/300px shows (42 > 30), 7d/200px does not (27 < 30).

## Changes required

### `server/internal/handlers/badges/service.go`

**`computeUptimeBarValues`** — add a `width int` parameter; replace the hardcoded `bucketDuration == 24h && n == 7` guard with a segment-width check:

```go
func computeUptimeBarValues(
    availMap map[time.Time]float64, bucketStart time.Time, n int, bucketDuration time.Duration,
    width int,
) []string {
    values := make([]string, n)

    // Only overlay text when segments are wide enough to fit it.
    segWidth := 0
    if n > 0 {
        segWidth = (width - (n - 1)) / n
    }
    if segWidth <= 30 {
        return values
    }

    for i := range n {
        t := bucketStart.Add(time.Duration(i) * bucketDuration)
        if pct, ok := availMap[t]; ok {
            values[i] = formatBarPercent(pct)
        }
    }

    return values
}
```

**`appendRowFragments`** — pass `width` to the updated function:

```go
barValues := computeUptimeBarValues(availMap, bucketStart, n, bucketDuration, width)
```

### `server/internal/handlers/badges/service_test.go`

Update `computeUptimeBarValues` call-sites to supply the new `width` argument. Add or extend table entries to cover:

- 7d / 300 px → values populated (42 px > 30 px)
- 7d / 200 px → values empty (27 px ≤ 30 px)
- 30d / 300 px → values empty (9 px ≤ 30 px)

No changes needed in `svg.go` — `renderUptimeBarRow` already guards on `barValues[idx] != ""`.

## Acceptance criteria

1. `GET /api/v1/orgs/{org}/checks/{check}/badges/status,availability,uptime-bar?period=7d` (default 300 px width) renders percentage labels inside each segment.
2. Same URL with `?period=7d&width=200` renders **no** percentage labels.
3. `?period=30d` (or `24h`, `90d`) at 300 px renders no percentage labels.
4. All existing badge tests pass.

## Implementation Plan

1. **`computeUptimeBarValues` signature + logic** (`server/internal/handlers/badges/service.go`)
   - Add a trailing `width int` parameter.
   - Replace the hardcoded `bucketDuration != 24*time.Hour || n != 7` guard with a
     segment-width computation: `segWidth = (width - (n - 1)) / n` (guarded for `n > 0`),
     and return the all-empty slice when `segWidth <= 30`.
   - Update the doc comment to describe the width-threshold behaviour instead of the
     7d-only assumption. The `bucketDuration` parameter is retained (still used to index
     `availMap`).

2. **`appendRowFragments` call-site** (`server/internal/handlers/badges/service.go`)
   - Pass `width` to `computeUptimeBarValues(...)`.

3. **Tests** (`server/internal/handlers/badges/service_test.go`)
   - Update the existing `TestComputeUptimeBarValues7d` call-site to pass a width of 300
     (segWidth 42 > 30 → values populated).
   - Add a case asserting 7d / 200 px → all-empty (segWidth 27 ≤ 30).
   - Update `TestComputeUptimeBarValuesNon7dIsEmpty` call-sites to pass width 300 and keep
     the all-empty assertions (24h → 11 px, 30d → 9 px, 90d → 3 px, all ≤ 30).
   - Optionally add an explicit width-threshold boundary table to make the rule obvious.

4. **QA**: `make build-backend build-client lint-back test`; fix until green.
   `svg.go` needs no change — `renderUptimeBarRow` already guards on `barValues[idx] != ""`.
