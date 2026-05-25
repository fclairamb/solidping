# Multi-row composable badge

## Context

Today the badges page produces **two unrelated images**: a single 20px
shields-style pill (`GET /badges/:components`, tokens
`status,availability,duration,response-time`) and a separate horizontal uptime
strip (`GET /uptime-bar`). They have separate config blocks, previews, embed
cards, and period/width params. There is **no response-time graph anywhere** in
the backend (only the flat pill and the segmented bar exist in `svg.go`).

We want one **stacked, composable badge image** of **1–3 rows**:

```
Row 1  (always)  check name as a black title, optionally followed by text metrics
Row 2  (opt)     uptime bar
Row 3  (opt)     average response-time graph
```

Worked examples:

```
Check 1                    Check 2              Check 3 | up 20% ↑1d 200ms
(Uptime Bar)               (Graph)              (Uptime Bar)
                                                (Graph)

Check 4
(Uptime Bar)
(Graph)
```

The tool is **not live**, so backward compatibility is a non-goal: the
standalone `/uptime-bar` endpoint is **removed** and folded in as a row. The
spec `2026-05-18-07-badge-uptime-history-bar.md` deliberately kept the bar as
a separate endpoint to preserve the shields pill's height contract; this spec
reverses that decision now that a unified multi-row image is the goal.

## URL design

```
GET /api/v1/orgs/:org/checks/:check/badges/:components
  ?period=24h|7d|30d|90d   (default: 30d)
  &width=<int>             (default: 300, range 60–800)
  &minWidth=<int>          (default: 0, range 0–800)
  &style=flat|flat-square  (default: flat)
  &label=<string>          (default: check name)
```

`period` controls: the availability/response-time window (row 1) AND the
bucket count for bar and graph rows (24h→24 hourly, 7d→7 daily, 30d→30 daily,
90d→90 daily — same as the existing `uptimeBarPeriodInfo` mapping).

`width` controls the combined image width when bar or graph rows are present;
row 1 stretches to fill `width` (existing `minWidth` value-cell stretch
mechanism). For text-only badges, natural pill width (respect `minWidth`) is
unchanged.

### Tokens

`:components` is a comma-separated, ordered subset of:

| Token | Row | Renders |
|---|---|---|
| `status` | 1 (text) | `up` / `down` / `unknown`; drives row-1 color |
| `availability` | 1 (text) | `99.9%` over `period` |
| `duration` | 1 (text) | `↑ 1d` up / `↓ 12m` down; omitted when unknown |
| `response-time` | 1 (text) | mean response time, e.g. `200ms` |
| `uptime-bar` | 2 | horizontal availability strip (N segments) |
| `response-time-graph` | 3 | filled-area average response-time graph |

### Validation (→ `400 VALIDATION_ERROR`)

1. Unknown token.
2. Duplicate token.
3. **Removed:** the old "at least one of status/availability" rule. Every
   token is optional. Row 1 always renders the check name, so
   `/badges/uptime-bar` (name + bar) and `/badges/response-time-graph` (name +
   graph) are valid. At least one token is required (path segment non-empty).

### Layout

Row order is fixed: row 1, then `uptime-bar` (if present), then
`response-time-graph` (if present). Absent rows are skipped (e.g. `Check 2`
stacks the graph directly under the title with no bar gap).

Heights: row1 = 20px, bar = 20px, graph = 40px, 4px gap between present rows.
`H = Σ(present row heights) + 4 × (rows − 1)`.

Row-1 styling:
- With ≥1 text metric token → existing shields `label|value` pill (gray name
  cell + colored value cell, space-joined values). Unchanged from today.
- With no text metric token → check name as a plain **black title** (no value
  cell), matching "possibly with only the black title".

Color precedence (unchanged): `status` present → green/red/gray by current
status; else `availability` present → threshold colors; else gray.

## SVG structure

Refactor `server/internal/handlers/badges/svg.go` from full `<svg>` emitters
to positioned `<g>` row fragments; a composer function assembles the outer
`<svg width=W height=H>`:

```
renderBadgeRow(label, value, color, style string, width, y int) string
  → <g transform="translate(0,y)">…shields content…</g>
  → black-title variant when value == ""

renderUptimeBarRow(segments []string, width, height int, style string, y int) string
  → <g transform="translate(0,y)">…clip-path + rects…</g>

renderResponseTimeGraphRow(points []*float64, width, height int, style string, y int) string
  → <g transform="translate(0,y)">…</g>
  → points: per-bucket averages oldest→newest, nil = no data (line breaks at gaps)
  → auto-scale Y to [min, max] with 10% padding
  → <defs> gradient fill (blue-ish to transparent); <path> area; <polyline> stroke
  → missing buckets produce a line break (separate polyline/path segments)

ComposeBadgeSVG(rows []string, W, H int) string
  → outer <svg>; joins pre-rendered row fragments
```

Keep `GenerateSVG` and `GenerateUptimeBarSVG` as thin wrappers (used by
tests) that delegate to the new row renderers + `ComposeBadgeSVG`.

## Backend implementation

Files: `server/internal/handlers/badges/`

### `service.go`

1. Extend `isAllowedComponent` / `parseComponents`:
   - Add `componentUptimeBar = "uptime-bar"` and
     `componentResponseTimeGraph = "response-time-graph"`.
   - **Remove** the `!seen[status] && !seen[availability]` guard.

2. Rework `GenerateBadge`:
   - Split parsed tokens into row-1 text tokens, `hasBar`, `hasGraph`.
   - Fetch row-1 data: existing `fetchResults` (unchanged).
   - Fetch bar/graph data when needed: aggregated bucket query (existing
     `uptimeBarPeriodInfo` logic), building:
     - `availMap map[time.Time]float64` (for bar, existing logic).
     - `durationMap map[time.Time]*float64` (for graph — `DurationAvg`; nil when
       bucket absent or `duration_avg` not yet populated).
   - Compute W: if `hasBar || hasGraph` → `opts.Width` (default 300); else
     natural pill width.
   - Compute H from row heights + gaps.
   - Render rows into `[]string` fragments; call `ComposeBadgeSVG`.

3. `applyDefaults`: switch default `period` from `"24h"` to `"30d"`.

4. `parsePeriod` / `uptimeBarPeriodInfo`: already handle `{24h,7d,30d,90d}`;
   drop `"1h"` from badge period validation (not useful for bucketed rows; not
   live so safe to drop).

5. **Delete** `GenerateUptimeBar`, `UptimeBarOptions` (logic lives inside
   `GenerateBadge`; the public `GetUptimeBar` handler is gone).

### `handler.go`

- Add `width` param parsing: `parseIntParam(..., 60, 800)`, default 300 when
  bar/graph tokens present.
- **Delete** `GetUptimeBar`.

### `server/internal/app/server.go`

Remove:

```go
api.GET("/orgs/:org/checks/:check/uptime-bar", badgesHandler.GetUptimeBar)
```

Keep:

```go
api.GET("/orgs/:org/checks/:check/badges/:components", badgesHandler.GetBadge)
```

## Aggregation — `duration_avg` column

The graph plots average response time per bucket. Aggregated rows store only
`duration_min/max/p95`; average is not persisted. Add it.

### Migration (`server/internal/migrations/`)

New migration file — add nullable column:

```sql
ALTER TABLE results ADD COLUMN duration_avg real;
```

### Model (`server/internal/db/models/result.go`)

```go
DurationAvg *float32 `bun:"duration_avg"`
```

### Aggregation job (`server/internal/jobs/jobtypes/job_aggregation.go`)

Populate `duration_avg` during rollup:
- **raw → hour:** `duration_avg = sum(raw.Duration) / count(raw with non-nil Duration)`.
- **hour/day → day/month:** `total_checks`-weighted mean of children's
  `duration_avg` (mirror existing `p95Sum` / `p95Count` pattern at ~line 957).

No backfill (not live). When no raw durations exist for a bucket, leave nil.

*Zero-migration alternative: plot `DurationP95` from existing columns. Rejected
because the requester explicitly chose average.*

## Frontend (`web/dash0/src/routes/orgs/$org/badges.tsx`)

### Search params (`BadgeSearch` interface)

Remove `barPeriod`, `barWidth`. Update / add:

```ts
components?: string   // default "status"
period?: "24h"|"7d"|"30d"|"90d"   // default "30d"
width?: number        // 60–800, default 300 (shown for bar/graph)
```

### `componentDefs` (canonically ordered checkboxes)

```ts
const componentDefs = [
  { token: "status",               labelKey: "components.status",           descKey: "..." },
  { token: "availability",         labelKey: "components.availability",      descKey: "..." },
  { token: "duration",             labelKey: "components.duration",          descKey: "..." },
  { token: "response-time",        labelKey: "components.responseTime",      descKey: "..." },
  { token: "uptime-bar",           labelKey: "components.uptimeBar",         descKey: "..." },
  { token: "response-time-graph",  labelKey: "components.responseTimeGraph", descKey: "..." },
] as const;
```

Remove the primary-required disable logic (`primaryCount === 1` → disabled).
When all tokens deselected, fall back to `"status"` in URL (keep the URL
non-empty).

### Controls visibility

- **Period select:** shown when any of `availability`, `response-time`,
  `uptime-bar`, `response-time-graph` is active.
- **Width input** (60–800): shown when `uptime-bar` or `response-time-graph` is
  active.
- **minWidth input** (0–800): shown when neither bar nor graph is active (text-
  only badges).

### Preview + embed card

- **Delete** `UptimeBarPreview`, its embed card, the `barPeriod`/`barWidth`
  selectors, and the border-t uptime bar sub-section in the config card.
- **Single** `BadgePreview` component + single embed card, pointing at the one
  combined URL. The preview `<img>` no longer hard-codes `h-5` (height is now
  variable; let the browser render the SVG's natural height).
- URL builder adds `width` when bar/graph active:
  ```ts
  if (hasRowToken && width !== 300) params.set("width", String(width));
  ```

### i18n (`locales/{en,fr,es,de}/badges.json`)

**Add:**
- `components.uptimeBar` / `components.uptimeBarDescription`
- `components.responseTimeGraph` / `components.responseTimeGraphDescription`
- `width` — label ("Width (px)")

**Remove:**
- `uptimeBar`, `uptimeBarDescription`, `uptimeBarPreview`, `uptimeBarEmbed`,
  `barWidth`, `components.required`

## Tests

### `service_test.go`

- All-six-tokens combination → correct multi-row SVG dimensions and content.
- `uptime-bar` alone (no text metric) → row 1 = black title, row 2 = bar.
- `response-time-graph` alone → row 1 = black title, row 3 = graph (no gap for row 2).
- Graph: all buckets present → N points; nil buckets → line break; single-
  value bucket → dot (no slope).
- Graph Y-scaling: min/max auto; padding applied.
- `parseComponents("uptime-bar")` → no error (primary no longer required).
- `parseComponents("unknown-token")` → `ErrInvalidFormat`.
- `parseComponents("status,status")` → `ErrInvalidFormat`.

### `test/integration/badges_test.go`

- `GET .../badges/status` → 200, `Content-Type: image/svg+xml`.
- `GET .../badges/status,availability,duration,response-time,uptime-bar,response-time-graph?period=30d` → 200.
- `GET .../badges/uptime-bar` → 200 (valid, name + bar).
- `GET .../uptime-bar` → **404** (route removed).
- `GET .../badges/unknown` → 400.

### Aggregation test

- `duration_avg` computed correctly for raw→hour rollup (mean of durations).
- `duration_avg` weighted correctly across hour→day.
- Bucket with no raw durations → `duration_avg` nil.

### Playwright (`web/dash0/e2e/badges.spec.ts`)

- Select a check → single combined preview appears.
- Toggle `uptime-bar` → URL gains `uptime-bar`, preview height increases.
- Toggle `response-time-graph` → URL gains `response-time-graph`, preview
  height increases again.
- Toggle `status` off (all others off too) → falls back to `status` in URL
  (no infinite-validation loop).
- Old uptime-bar section, `UptimeBarPreview` img, uptime-bar embed URL and
  period select are **absent from the DOM**.
- Download SVG/PNG of a multi-row badge.

## Out of scope

- Per-segment tooltips or axis labels on the graph (visual-only badge).
- Region filtering (org-wide, as today).
- Backfilling `duration_avg` for historical data.
- Keeping `1h` as a valid badge period.

## Acceptance criteria

- [ ] `GET .../badges/status` returns the same 20px pill as today.
- [ ] `GET .../badges/uptime-bar` returns name-title row + bar strip; `H = 44`.
- [ ] `GET .../badges/response-time-graph` returns name-title row + graph; `H = 64`.
- [ ] `GET .../badges/status,uptime-bar,response-time-graph` returns 3-row image; `H = 88`.
- [ ] Row 1 with no text metrics renders the check name in black (no gray label cell).
- [ ] Period controls segment count: `period=7d` → 7 bar segments; `period=90d` → 90.
- [ ] `GET .../uptime-bar` returns 404.
- [ ] `duration_avg` populated on new aggregated rows; graph reads it per bucket.
- [ ] Nil-bucket slots produce a line break in the graph, not a zero-value point.
- [ ] Frontend: 6 checkboxes; width shown for bar/graph; old uptime-bar UI absent.
- [ ] All backend tests pass: `cd server && go test ./internal/handlers/badges/... ./internal/jobs/... -v`
- [ ] Integration: `cd server && go test ./test/integration -run Badge -v`
- [ ] Frontend: `cd web/dash0 && bun run lint && bun run build && bunx playwright test e2e/badges.spec.ts`

```bash
# Quick smoke (server on :4000)
curl -si 'http://localhost:4000/api/v1/orgs/default/checks/<slug>/badges/status,availability,uptime-bar,response-time-graph?period=30d'
curl -si 'http://localhost:4000/api/v1/orgs/default/checks/<slug>/uptime-bar'   # expect 404
```

## Implementation Plan

### Step 1 — `duration_avg` column (migration + model)
- New migration `031_result_duration_avg` (postgres + sqlite up/down):
  `ALTER TABLE results ADD COLUMN duration_avg real;` plus a postgres comment;
  down drops the column.
- Add `DurationAvg *float32 \`bun:"duration_avg"\`` to `models.Result`.

### Step 2 — Aggregation job populates `duration_avg`
- `aggregationState`: add `durationAvgSum float32`, `durationAvgCount int` (for
  weighted aggregation), and track an `avgDuration` for raw.
- raw → hour: `duration_avg = sum(raw.Duration)/count(non-nil Duration)` (this
  is the existing `avgDuration` from `calculateRawMetrics`).
- hour/day → day/month: `total_checks`-weighted mean of children's
  `DurationAvg` — `Σ(child.DurationAvg × child.TotalChecks) / Σ(TotalChecks
  over children with non-nil DurationAvg)`. Nil when no children carry it.
- `buildAggregatedResult` sets `DurationAvg` (nil when count == 0).
- Update `TestAggregateResults_RawData` and add tests for weighted hour→day +
  nil-bucket.

### Step 3 — SVG row renderers (`svg.go`)
- Keep color consts + `escapeXML`.
- Add `renderBadgeRow(label, value, color, style string, width, y int) string`
  → `<g transform>` shields content; black-title variant when `value == ""`.
- Add `renderUptimeBarRow(segments []string, width, height, y int, style string) string`.
- Add `renderResponseTimeGraphRow(points []*float64, width, height, y int, style string) string`
  with auto Y-scale (10% padding), gradient `<defs>`, area `<path>`, stroke
  `<polyline>`, line breaks at nil gaps, single-point dot.
- Add `ComposeBadgeSVG(rows []string, w, h int) string`.
- Rewrite `GenerateSVG` / `GenerateUptimeBarSVG` as thin wrappers over the row
  renderers + `ComposeBadgeSVG` (keep test signatures).

### Step 4 — Service composition (`service.go`)
- Add token consts `componentUptimeBar`, `componentResponseTimeGraph`;
  extend `isAllowedComponent`.
- `parseComponents`: drop the status/availability-required guard (keep unknown
  + duplicate + non-empty checks).
- Rework `GenerateBadge`: split tokens into text tokens / hasBar / hasGraph;
  fetch row-1 data via `fetchResults`; when bar/graph present, run the bucket
  query building `availMap` and `durationMap`; compute W (300 default when
  bar/graph) and H (20/20/40 rows + 4px gaps); render row fragments; compose.
- `applyDefaults`: default period `"30d"`.
- `parsePeriod`: drop `"1h"`, default `"30d"`.
- Delete `GenerateUptimeBar` + `UptimeBarOptions`.

### Step 5 — Handler + route
- `handler.go`: parse `width` (60–800, default 300 when bar/graph present);
  delete `GetUptimeBar`.
- `server.go`: remove the `/uptime-bar` route.

### Step 6 — Backend tests
- `service_test.go`: all-six-tokens dimensions; `uptime-bar`-only (black
  title + bar); `response-time-graph`-only (no bar gap); graph points / nil
  break / single dot / Y-scaling; updated `parseComponents` expectations;
  update `parsePeriod`/`uptimeBarPeriodInfo` tests for dropped `1h`.
- `test/integration/badges_test.go`: add bar/graph 200 cases, `/uptime-bar`
  → 404, fix the now-valid `duration`-only case, drop `1h` from period list.

### Step 7 — Frontend (`badges.tsx` + i18n + routeTree)
- `BadgeSearch`: drop `barPeriod`/`barWidth`; add `width`; period type
  `24h|7d|30d|90d` default `30d`; valid tokens set gains `uptime-bar`,
  `response-time-graph`.
- `componentDefs`: 6 entries; remove primary-required disable + fallback to
  `status` when empty.
- Controls: period shown for availability/response-time/uptime-bar/
  response-time-graph; width (60–800) shown for bar/graph; minWidth shown only
  for text-only.
- Delete `UptimeBarPreview` + its embed card + the bar sub-section; single
  `BadgePreview`; preview `<img>` drops hard-coded `h-5`; URL builder adds
  `width` when bar/graph active and `!= 300`.
- i18n add/remove keys across en/fr/es/de.

### Step 8 — Playwright (`e2e/badges.spec.ts`)
- Replace primary-required tests with: toggle uptime-bar / response-time-graph
  grows preview + URL gains token; toggling all off falls back to `status`;
  old uptime-bar section / img / embed absent; multi-row SVG/PNG download.
- Add `badge-component-uptime-bar` / `badge-component-response-time-graph` /
  `badge-width` testids; regenerate route tree if needed.

### Step 9 — QA + audit
- `make fmt`; `make build-backend build-client lint-back test`; fix; audit.
