# Badge uptime history bar

## Goal

Add a standalone uptime history bar — a horizontal strip of N colored segments
representing availability per time bucket — served as SVG from a dedicated endpoint.

This is the "graph of last stats" that makes sense as a badge: it shows the time
dimension at a glance, making one bad hour or day visible without having to open the
dashboard.

## Why a separate endpoint, not a component

The composable badge (`/badges/:components`) is a fixed 20px-tall shields.io-style
label|value pill. Adding a graph *below* the existing badge would break the height
contract that every embedding site (GitHub README, Notion, Confluence) relies on.

A dedicated endpoint (`/uptime-bar`) returns a different SVG shape — wider, same
height, no text — that can be placed *next to* the standard badge in a README or
table. Clean separation of concerns; existing badge URLs are unaffected.

## Endpoint

```
GET /api/v1/orgs/:org/checks/:check/uptime-bar
  ?period=30d|90d|7d|24h   (default: 30d)
  &width=<int>              (default: 300, range 60–800)
  &height=<int>             (default: 20, range 10–40)
  &style=flat|flat-square   (default: flat)
```

Response: `Content-Type: image/svg+xml`, `Cache-Control: public, max-age=300`.

## Visual design

```
┌──┬──┬──┬──┬──┬──┬──┬──┬──┬──┬──┬──┬──┬──┬──┬──┬──┬──┬──┬──┐
│  │  │  │  │  │  │  │  │  │  │  │  │  │  │  │  │  │  │  │  │  <- 30 daily segments
└──┴──┴──┴──┴──┴──┴──┴──┴──┴──┴──┴──┴──┴──┴──┴──┴──┴──┴──┴──┘
```

- N segments, 1px gap between each, rounded corners matching `style`.
- Segments are colored left-to-right from oldest to newest.
- The rightmost segment is the current (potentially incomplete) bucket.

## Segment count and data source

| `period` | Segments (N) | `period_type` queried |
|---|---|---|
| `24h` | 24 | `hour` |
| `7d` | 7 | `day` |
| `30d` | 30 | `day` |
| `90d` | 90 | `day` |

## Color mapping

| Condition | Color |
|---|---|
| No data for bucket | `#9f9f9f` (gray) |
| `availability_pct >= 99.9` | `#4c1` (green) |
| `availability_pct >= 99` | `#dfb317` (yellow) |
| `availability_pct >= 98` | `#fe7d37` (orange) |
| `availability_pct < 98` | `#e05d44` (red) |

Reuse the existing `availabilityColor` helper (currently private — export it or move it
to a shared location).

## SVG structure

```xml
<svg xmlns="http://www.w3.org/2000/svg" width="300" height="20">
  <clipPath id="a">
    <rect width="300" height="20" rx="3" fill="#fff"/>
  </clipPath>
  <g clip-path="url(#a)">
    <!-- one rect per bucket, colored -->
    <rect x="0"   width="9" height="20" fill="#4c1"/>
    <rect x="10"  width="9" height="20" fill="#4c1"/>
    ...
    <rect x="290" width="9" height="20" fill="#9f9f9f"/>
  </g>
</svg>
```

Segment width = `floor((totalWidth - (N-1)) / N)` (subtract 1px gaps).
Remaining pixels from rounding are distributed to the last segment.

## Backend

### `service.go` — new method `GenerateUptimeBar`

```go
func (s *Service) GenerateUptimeBar(
    ctx context.Context, orgSlug, checkIdentifier string, opts UptimeBarOptions,
) (string, error)
```

`UptimeBarOptions`:
```go
type UptimeBarOptions struct {
    Period string // "24h", "7d", "30d", "90d"
    Width  int    // px, default 300
    Height int    // px, default 20
    Style  string // "flat", "flat-square"
}
```

Steps:
1. Resolve org + check (same pattern as `GenerateBadge`).
2. Determine `periodType` and `N` from `Period`.
3. Compute `bucketStart = time.Now().Truncate(bucketDuration).Add(-N * bucketDuration)`.
4. Fetch aggregated results: `period_type = periodType`, `period_start >= bucketStart`.
5. Build a `map[time.Time]float64` of `period_start → availability_pct`.
6. Iterate the N buckets in order; look up each in the map (gray if missing).
7. Call `GenerateUptimeBarSVG(segments, opts)`.

### `svg.go` — new function `GenerateUptimeBarSVG`

```go
func GenerateUptimeBarSVG(segments []string, width, height int, style string) string
```

`segments` is a slice of hex color strings in chronological order.

### `handler.go` — new method `GetUptimeBar`

Parse query params, apply defaults and clamping, call `svc.GenerateUptimeBar`,
write `image/svg+xml` response.

### `server/internal/app/server.go`

```go
api.GET("/orgs/:org/checks/:check/uptime-bar", badgesHandler.GetUptimeBar)
```

(Public — no auth middleware, same as the existing `/badges/:components` route.)

## Frontend (`web/dash0/src/routes/orgs/$org/badges.tsx`)

### Configuration card additions

Add an **Uptime Bar** section below the existing component checkboxes, visible only
when a check is selected. Controls:
- Period select (same four options, default `30d`)
- Width number input (default 300, range 60–800)

Keep it visually distinct from the badge configuration section with a separator or
sub-heading.

### Preview

Add a second preview card "Uptime Bar Preview" below the existing "Badge Preview"
card, showing the bar SVG in the same dashed container.

### Embed code

A separate embed code card "Uptime Bar Embed" with URL, Markdown, and HTML blocks
for the uptime bar URL.

### i18n (`locales/{en,fr,es,de}/badges.json`)

Add:
- `uptimeBar` — section heading ("Uptime Bar")
- `uptimeBarDescription` — ("A horizontal strip showing availability per time bucket")
- `uptimeBarPreview` — card title ("Uptime Bar Preview")
- `uptimeBarEmbed` — card title ("Uptime Bar Embed Code")
- `barWidth` — label ("Bar width (px)")

## Tests

### `service_test.go`

- All N buckets present → correct colors, no gray segments.
- Bucket with no data → gray segment.
- Mixed: some buckets missing → correct positions are gray.
- `availability_pct` thresholds → each color boundary (99.9, 99, 98).
- Incomplete current bucket (< full period elapsed) → not gray unless truly no data.

### `integration/badges_test.go` (or new `uptime_bar_test.go`)

- `GET .../uptime-bar` → 200, `Content-Type: image/svg+xml`.
- `GET .../uptime-bar?period=90d&width=600` → 200.
- Unknown check → 404.

### Playwright (`web/dash0/e2e/badges.spec.ts`)

- Select a check → uptime bar preview img appears.
- Uptime bar embed URL contains `/uptime-bar`.

## Out of scope

- Text overlay on the bar (e.g., "99.7% uptime") — omit for now; the bar is
  intentionally visual-only and pairs with the standard badge for the number.
- Tooltip per segment — SVG `<title>` is an option for accessibility but not required.
- Region filtering — bars always show org-wide availability (same as the badge).
- Raw-only `24h` fallback when hourly aggregates don't exist yet — return gray bar with
  a comment in the SVG; don't fall back to raw results.

## Acceptance criteria

- [ ] `GET .../uptime-bar` returns a valid SVG with N colored `<rect>` elements.
- [ ] `period=90d` produces 90 segments; `period=24h` produces 24.
- [ ] Buckets with no aggregated data are rendered in gray.
- [ ] Color thresholds match availability color logic used by the existing badge.
- [ ] Width and height query params control the SVG dimensions (clamped to range).
- [ ] `style=flat-square` produces `rx="0"` on the clip path rect.
- [ ] Frontend shows uptime bar preview and embed code when a check is selected.
- [ ] Badge configuration page URL linking to the bar works standalone (curl test).
