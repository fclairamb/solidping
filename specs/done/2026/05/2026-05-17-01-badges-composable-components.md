# Composable badge components

## Goal

Replace the three hard-coded badge formats (`status`, `availability`,
`availability-duration`) with a composable URL scheme that lets users mix and
match up to four independent metric components:

```
GET /api/v1/orgs/:org/checks/:check/badges/:components
```

where `:components` is a comma-separated ordered subset of
`status`, `availability`, `duration`, `response-time`.

The badges configuration page gains four checkboxes instead of a Format
select. The Download card is removed; SVG and PNG download buttons move into
the Preview card header.

## Why

Three fixed formats scale poorly. `availability-duration` was already the
first combinatorial compound; adding "average response time" or "status
duration" would have produced an explosion of kebab-named tokens
(`availability-duration-response-time`, …). Composable components scale
linearly: four tokens yield fifteen non-empty subsets, all working without
any new backend formats.

Additionally, the Download card was a structural redundancy — three format
buttons sitting below a preview with no relationship to the Configuration
panel. Moving SVG + PNG buttons into the preview header puts the action next
to the artifact.

## URL design

```
GET /api/v1/orgs/:org/checks/:check/badges/:components
  ?period=1h|24h|7d|30d   (default: 24h)
  &label=<string>          (default: check name)
  &style=flat|flat-square  (default: flat)
```

### Allowed component tokens (kebab-case, consistent with existing `availability-duration`)

| Token | Metric |
|---|---|
| `status` | Current status: `up` / `down` / `unknown` |
| `availability` | % availability over `period` |
| `duration` | Time since last status change: `↑ 3d` when up, `↓ 12m` when down, omitted when unknown |
| `response-time` | Mean response time over `period`: e.g. `250ms` |

### Validation rules (all → `400 VALIDATION_ERROR`)

1. Unknown token in the list.
2. Duplicate token in the list.
3. Neither `status` nor `availability` is present — at least one "primary"
   metric is required to convey health.

### Value composition

The value pane of the SVG badge is the selected components joined by a single
space in URL order:

```
status + availability + duration + response-time  →  "up 99.95% ↑ 3d 250ms"
availability + response-time                      →  "99.95% 250ms"
status + duration                                 →  "up ↑ 3d"
```

### Color rule (precedence unchanged)

`status` present → green/red/gray based on current status.  
`availability` only → existing threshold colors (≥99.9% green, ≥99% yellow, ≥98% orange, else red).  
Neither → gray.

### `period` applicability

`period` gates time-windowed metrics: `availability` and `response-time`.
`status` uses the single latest raw result. `duration` uses the elapsed time
since the last status change (computed from the same result window, not
bounded by `period`).

### Breaking change

The legacy `:format` path `availability-duration` is **not** preserved as an
alias. Requests to it return `400 VALIDATION_ERROR`. Existing embedded badges
must update to `availability,duration`.

## Backend implementation

Files live in `server/internal/handlers/badges/`.

### `service.go` — replace the switch with a composition pipeline

1. Add `parseComponents(raw string) ([]string, error)`:
   - Split on `,`.
   - Validate each token is in the allowed set.
   - Validate no duplicates.
   - Validate at least one of `status`/`availability` is present.
   - Return the ordered list.

2. Rewrite `GenerateBadge`:
   - Parse components.
   - Determine the widest fetch needed:
     - If `availability` or `response-time` → fetch raw results over `period`.
     - If `status` or `duration` only → fetch latest 1 raw result.
     - If both → merge (one fetch with `PeriodStartAfter` suffices; status
       reads from `results[0]`).
   - For each component in order, compute its substring.
   - Join substrings with single space.
   - Resolve badge color (precedence above).
   - Return `GenerateSVG(label, value, color, style)`.

3. New helper `formatResponseTime(results []*models.Result) string`:
   - Mean of non-nil `Duration` fields (float32 seconds).
   - Format: `<N>ms` when mean < 1 s, else `<N.N>s`.

4. Extend `calculateUptimeDuration` (or add `calculateStatusDuration`) to
   return both the duration and direction so the `duration` component can
   render `↑` vs `↓`.

### `handler.go`

- Read `req.Param("components")` instead of `req.Param("format")`.
- Pass the raw string to `svc.GenerateBadge` unchanged.

### `server/internal/app/server.go` (line 533)

```go
api.GET("/orgs/:org/checks/:check/badges/:components", badgesHandler.GetBadge)
```

### Tests

`server/internal/handlers/badges/service_test.go`:
- Each valid single component renders correctly.
- Multi-component composition respects URL order.
- `availability-duration` (legacy) → `ErrInvalidFormat`.
- Unknown token → `ErrInvalidFormat`.
- Duplicate token → `ErrInvalidFormat`.
- Missing both status and availability → `ErrInvalidFormat`.
- `response-time` mean math.
- `duration` direction: `↑` when last result is up, `↓` when down, omitted when unknown.

`server/test/integration/badges_test.go`:
- `GET .../badges/status` → 200.
- `GET .../badges/status,availability,duration,response-time` → 200.
- `GET .../badges/availability-duration` → 400.
- `GET .../badges/duration` → 400 (missing primary).

## Frontend implementation

File: `web/dash0/src/routes/orgs/$org/badges.tsx`.

### Search-param schema

- Remove `format: BadgeFormat`.
- Add `components: string` (comma-separated canonical string, default `"status"`).
- `validateSearch`: parse the comma list, drop unknown tokens, enforce the
  "primary" rule by falling back to `"status"` if neither status nor
  availability is present.
- `updateSearch`: strip the URL param when value equals the default `"status"`.

### Configuration card

Remove the Format `Select` block. Replace with a **Components** block
containing four `Checkbox` rows in canonical order:

1. Status (checked by default)
2. Availability
3. Duration
4. Response time

Import `Checkbox` from `@/components/ui/checkbox` (already exists, in the
design reference).

Constraint enforcement — make it un-violatable via UI:
- Compute `primaryCount` = count of `status` + `availability` checked.
- If `primaryCount === 1` and the user tries to uncheck the sole primary, the
  checkbox is `disabled` while checked (prevent unchecking the last one).
- Show a muted helper text under the group: "At least one of Status or
  Availability is required."

Period `Select` visibility: show when `availability` OR `response-time` is
selected (replacing the current `format !== "status"` guard).

### URL builder

```ts
const badgePath = `/api/v1/orgs/${org}/checks/${identifier}/badges/${components}${query ? `?${query}` : ""}`;
```

### Preview card

Keep the preview `<img>` and Refresh button. Add two buttons to the card
header **before** Refresh:

- **SVG** — calls `downloadBadge("svg")`.
- **PNG** — calls `downloadBadge("png")`.

`downloadBadge` is unchanged for SVG and PNG branches. Drop the JPG branch.
Download filename: `${identifier}-${components.replace(/,/g, "-")}.${ext}`.

Remove the `Image` and `FileImage` Lucide imports if they are no longer used
elsewhere in the file. Add `FileDown` or reuse `Download` for the new buttons.

### Download card

Delete entirely (lines 220–241 in the current file).

### Embed Code card

Switch the markdown alt text from `${check.name || identifier} ${format}` to
`${check.name || identifier} badge` — the format list in alt text was
awkward and is now a comma-separated string.

## i18n

Four locale files: `web/dash0/src/locales/{en,fr,es,de}/badges.json`.

**Remove:**
- `formats`, `formats.status`, `formats.statusDescription`, `formats.availability`, `formats.availabilityDescription`, `formats.availabilityDuration`, `formats.availabilityDurationDescription`
- `download`, `downloadDescription`

**Add:**
- `components` — section label ("Components")
- `components.required` — helper text ("At least one of Status or Availability is required")
- `components.status` — checkbox label
- `components.statusDescription` — muted description (right side or tooltip)
- `components.availability` — checkbox label
- `components.availabilityDescription`
- `components.duration` — checkbox label
- `components.durationDescription` — ("Time since last status change")
- `components.responseTime` — checkbox label
- `components.responseTimeDescription` — ("Mean response time over the selected period")
- `downloadSvg` — button label ("SVG")
- `downloadPng` — button label ("PNG")

Keep `downloadFailed` — still used by the inline download buttons.

## Playwright E2E (`web/dash0/e2e/badges.spec.ts`)

- Check selector → pick a check → preview image visible.
- Toggle Availability checkbox on → preview updates (new URL contains `status,availability`).
- Try to uncheck Status when it's the only primary → checkbox remains checked (or is disabled).
- Uncheck Status, check Availability → works; preview shows `availability` URL.
- SVG download button → file saved.
- PNG download button → file saved.
- Confirm no Download card element exists in the DOM.
- Period selector visible when Availability is checked; hidden when only Status + Duration are checked.

## Out of scope

- PNG rendering server-side. The server stays SVG-only; client-side canvas
  conversion handles PNG download.
- JPG download — dropped along with the Download card. Not enough demand to
  justify the complexity.
- Backwards-compatible alias for `availability-duration`.
- Any change to the SVG template in `svg.go`.

## Verification checklist

```bash
# Backend
cd server && go test ./internal/handlers/badges/... -v
cd server && go test ./test/integration -run Badge -v

# Quick smoke against running server
curl -si 'http://localhost:4000/api/v1/orgs/default/checks/docker-postgres/badges/status'
curl -si 'http://localhost:4000/api/v1/orgs/default/checks/docker-postgres/badges/status,availability,duration,response-time?period=7d'
curl -si 'http://localhost:4000/api/v1/orgs/default/checks/docker-postgres/badges/availability-duration'  # expect 400
curl -si 'http://localhost:4000/api/v1/orgs/default/checks/docker-postgres/badges/duration'               # expect 400

# Frontend
cd web/dash0 && bun run lint && bun run build
cd web/dash0 && bunx playwright test e2e/badges.spec.ts
```

Browser:
- `/dash0/orgs/default/badges?check=docker-postgres`
- Toggle each checkbox, verify URL and preview update.
- Attempt to uncheck the sole checked primary → blocked.
- SVG and PNG download buttons produce correct files.
- No "Download" card exists on the page.
