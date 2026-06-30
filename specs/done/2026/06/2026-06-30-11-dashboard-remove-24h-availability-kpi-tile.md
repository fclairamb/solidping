# Remove the "24h availability" KPI tile from the org dashboard

## Context

The operator org dashboard (`/dash0/orgs/$org`, e.g.
`http://localhost:4000/dash0/orgs/default`) renders a row of **four** KPI tiles
at the top:

1. **Monitored checks** — links to `/orgs/$org/checks`
2. **Currently down** — links to `/orgs/$org/checks?status=down`
3. **Active incidents** — links to `/orgs/$org/incidents?state=active`
4. **24h availability** — a static (non-link) metric tile showing
   `{availabilityPct}%`, or `—` with the sub-label **"No data yet"** when there
   is no aggregated result data.

Source: [`web/dash0/src/components/dashboard/dashboard-page.tsx`](../../web/dash0/src/components/dashboard/dashboard-page.tsx),
tile block at
[`dashboard-page.tsx:412-419`](../../web/dash0/src/components/dashboard/dashboard-page.tsx#L412):

```tsx
<div data-testid="kpi-tile-availability">
  <KpiTile
    label={t("kpi.availability")}
    value={availabilityPct === null ? "—" : `${availabilityPct.toFixed(2)}%`}
    icon={<CheckCircle className="h-4 w-4 text-muted-foreground" />}
    sub={availabilityPct === null ? t("kpi.availabilityNoData") : undefined}
  />
</div>
```

The four tiles sit in a responsive grid declared at
[`dashboard-page.tsx:353`](../../web/dash0/src/components/dashboard/dashboard-page.tsx#L353):

```tsx
<div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
```

The user asked to **remove the "24h availability" tile** (see screenshot: the
card titled "24h availability" showing "No data yet"). It frequently shows the
empty `—` / "No data yet" state on fresh or low-traffic orgs, which reads as a
broken or unpopulated widget rather than useful information.

### The underlying availability value is shared with the banner

`availabilityPct` is computed once at
[`dashboard-page.tsx:270`](../../web/dash0/src/components/dashboard/dashboard-page.tsx#L270)
via `weightedAvailability(results)` and is consumed by **two** places:

- the KPI tile (lines 414-417), **and**
- the green "All systems operational" `OverallStatusBanner`
  ([`dashboard-page.tsx:350`](../../web/dash0/src/components/dashboard/dashboard-page.tsx#L350)),
  which renders the inline phrase **"{count} checks monitored, {availability}%
  uptime over 24h"** (i18n key `banner.allGreenSub`, a different string from the
  tile's `kpi.availability`).

So removing the tile must **not** remove `availabilityPct`, `weightedAvailability`,
or the day-granularity `resultsQuery` — the banner still needs them. Only the
tile is deleted.

## Decision

Delete the 4th KPI tile (`kpi-tile-availability`) and reflow the grid from 4
columns to 3 so the three remaining link tiles (Monitored / Down / Incidents)
fill the desktop row evenly. Keep the availability percentage in the green
status banner unchanged — it is a separate UI element, not the "24h
availability" card the user pointed at.

## Goals

- The org dashboard shows **3** KPI tiles: Monitored checks, Currently down,
  Active incidents. No "24h availability" tile.
- On desktop (`lg`), the three tiles span the row evenly (no empty 4th column /
  ragged gap). On tablet (`md`) and mobile, layout stays clean and fully usable.
- The green "All systems operational" banner still shows
  "{count} checks monitored, {availability}% uptime over 24h" exactly as before.
- No dead code: the now-unused i18n keys (`kpi.availability`,
  `kpi.availabilityNoData`) are removed from all four locales; no orphaned
  imports are left behind.
- Lint, type-check, and the dash0 E2E suite stay green.

## Out of scope

- **The status banner's availability text** — the "% uptime over 24h" phrase in
  `OverallStatusBanner` (`banner.allGreenSub`) stays. Do **not** remove
  `availabilityPct`, `weightedAvailability(...)`, or the day `resultsQuery` that
  feeds it.
- The other three KPI tiles, their link targets, the glance card, incidents
  list, recent activity feed, and every other dashboard section.
- The check-detail availability table / uptime strips
  (`web/dash0/src/components/checks/availability-table.tsx`,
  `components/ui/uptime-strip.tsx`) and the server-side availability work in
  the sibling specs (`2026-06-30-10-check-availability-server-side-statistics-api`).
  Those are unrelated to this dashboard tile.
- The design-reference page's own `KpiTile` demo
  ([`design-reference.tsx`](../../web/dash0/src/routes/orgs/$org/design-reference.tsx),
  ~line 1774-1821), which illustrates the static-metric tile pattern with its own
  mock data. It demonstrates the reusable primitive and remains valid; leave it.
- The legacy `web/dash` app.

## Implementation

### 1. Remove the tile and reflow the grid

In [`web/dash0/src/components/dashboard/dashboard-page.tsx`](../../web/dash0/src/components/dashboard/dashboard-page.tsx):

- Delete the entire `<div data-testid="kpi-tile-availability">…</div>` block
  (lines ~412-419), including the `<KpiTile>` it wraps.
- Change the grid container (line ~353) from `lg:grid-cols-4` to `lg:grid-cols-3`:

  ```tsx
  <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
  ```

  (`md:grid-cols-2` is fine for three tiles — 2 + 1. Optionally use
  `md:grid-cols-3` if a single tablet row is preferred; `lg:grid-cols-3` is the
  required change.)

### 2. Keep everything the banner needs

Do **not** touch:

- `const availabilityPct = weightedAvailability(results);` (line ~270)
- the `weightedAvailability` function (lines ~186-197)
- the day-granularity `resultsQuery` (lines ~220-225)
- the `availabilityPct={availabilityPct}` prop passed to `OverallStatusBanner`
  (line ~350) and the banner's `banner.allGreenSub` rendering (lines ~563-567)

### 3. Imports

Verify no import is orphaned by the deletion. In particular `CheckCircle` (the
tile's icon) is **also** used by the green banner
([`dashboard-page.tsx:558`](../../web/dash0/src/components/dashboard/dashboard-page.tsx#L558)),
so its import stays. `KpiTile` remains used by the other three tiles. Remove an
import **only** if a build/lint check proves it is now unused (none is expected).

### 4. Remove unused i18n keys

The `kpi.availability` and `kpi.availabilityNoData` keys are used only by the
deleted tile (confirmed: the banner uses the separate `banner.allGreenSub`
string with an `{{availability}}` interpolation variable, not these keys). Delete
both keys from all four locale files:

- [`web/dash0/src/locales/en/dashboard.json`](../../web/dash0/src/locales/en/dashboard.json) (lines 51-52)
- [`web/dash0/src/locales/fr/dashboard.json`](../../web/dash0/src/locales/fr/dashboard.json) (lines 51-52)
- [`web/dash0/src/locales/de/dashboard.json`](../../web/dash0/src/locales/de/dashboard.json) (lines 51-52)
- [`web/dash0/src/locales/es/dashboard.json`](../../web/dash0/src/locales/es/dashboard.json) (lines 51-52)

Take care with trailing commas — `availabilityNoData` is the last key in the
`kpi` object, so the preceding `incidentsSubNone` entry must end without a
trailing comma after the removal, keeping each file valid JSON.

## Design reference

No new primitive and no change to the canonical components. This removes one
instance of the existing `KpiTile`; the design-reference page keeps its own
`KpiTile` example, so the catalog at
[`web/dash0/src/routes/orgs/$org/design-reference.tsx`](../../web/dash0/src/routes/orgs/$org/design-reference.tsx)
stays accurate and needs no edit.

## Verification

With `make dev-test` running, open `http://localhost:4000/dash0/orgs/test`
(and `/dash0/orgs/default` in normal mode):

1. The top KPI row shows exactly **3** tiles — Monitored checks, Currently down,
   Active incidents — with **no** "24h availability" tile.
2. On a wide desktop viewport the three tiles span the full row evenly (no empty
   4th slot). Resize down to tablet and mobile widths — layout stays clean,
   nothing overflows, tiles remain tappable.
3. For a healthy org (no down checks, no incidents), the green "All systems
   operational" banner still reads "{N} checks monitored, {X.XX}% uptime over
   24h" — the availability number is unchanged.
4. No console errors; switching UI language (en/fr/de/es) shows no missing-key
   warnings for the dashboard namespace.

## Tests

- Update the dashboard E2E
  ([`web/dash0/e2e/dashboard.spec.ts`](../../web/dash0/e2e/dashboard.spec.ts)):
  remove the assertion block at lines ~288-292 that fetches
  `getByTestId("kpi-tile-availability")` and asserts it is visible and not an
  `<a>`. The surrounding test ("KPI tiles link to filtered views") otherwise
  stands — it still verifies the three remaining link tiles navigate correctly.
  Confirm no other test references `kpi-tile-availability`,
  `kpi.availability`, or the "24h availability" label (grep first).
- `cd web/dash0 && bun run lint` and `bun run build` (tsc) pass with no **new**
  errors (gate against the known ~25-error react-hooks base debt — no new ones).
- `make test-dash` (Playwright) stays green.

## Files referenced

- [`web/dash0/src/components/dashboard/dashboard-page.tsx`](../../web/dash0/src/components/dashboard/dashboard-page.tsx)
  — remove the `kpi-tile-availability` block (~412-419); change grid
  `lg:grid-cols-4` → `lg:grid-cols-3` (~353). Leave `availabilityPct`,
  `weightedAvailability`, `resultsQuery`, the banner prop, and the `CheckCircle`
  import intact.
- [`web/dash0/src/locales/en/dashboard.json`](../../web/dash0/src/locales/en/dashboard.json),
  [`fr`](../../web/dash0/src/locales/fr/dashboard.json),
  [`de`](../../web/dash0/src/locales/de/dashboard.json),
  [`es`](../../web/dash0/src/locales/es/dashboard.json) — delete `kpi.availability`
  and `kpi.availabilityNoData`; keep `banner.allGreenSub`.
- [`web/dash0/e2e/dashboard.spec.ts`](../../web/dash0/e2e/dashboard.spec.ts)
  — drop the availability-tile assertion (~288-292).

## Implementation Plan

1. **Edit `dashboard-page.tsx`**: delete the `kpi-tile-availability` `<div>`/
   `<KpiTile>` block; change the grid to `md:grid-cols-2 lg:grid-cols-3`. Leave
   the availability computation, the banner wiring, and all imports
   (`CheckCircle`, `KpiTile`, …) untouched — they remain in use.
2. **Edit the four `dashboard.json` locales**: remove `kpi.availability` and
   `kpi.availabilityNoData`, fixing the trailing comma so each file stays valid
   JSON. Keep `banner.allGreenSub`.
3. **Update `dashboard.spec.ts`**: remove the availability-tile visibility/not-a-
   link assertion. Grep to confirm no remaining references to
   `kpi-tile-availability` / `kpi.availability` / "24h availability".
4. **Format & verify**: `make fmt`; then `cd web/dash0 && bun run lint` and
   `bun run build` (no new errors), and `make test-dash`.
5. **Manual QA**: per the Verification section — 3 tiles, even desktop row,
   responsive on mobile, banner availability text unchanged, no missing-i18n-key
   warnings across locales.
6. **Archive** the spec to `specs/done/2026/06/`.
