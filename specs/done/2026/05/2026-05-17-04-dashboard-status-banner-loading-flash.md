# Fix dashboard status banner flashing green before check data loads

## Context

On `/dash0/orgs/$org`, the `OverallStatusBanner` briefly renders the green
"All systems operational" card right after page load, then flips to red once
check results arrive. The flash happens because `isInitialLoading` uses `&&`
(`dashboard-page.tsx:161-165`): as soon as the fastest query resolves
(typically `incidentsQuery`), the gate turns false and the banner renders with
`checks = []` still empty — `downCount === 0 && incidentsCount === 0` →
`allGreen = true` → green card. When `checksQuery` then resolves with actual
data, the banner re-renders red.

```ts
// BEFORE (line 161) — wrong operator
const isInitialLoading =
  checksQuery.isLoading &&   // ← one resolves → false → banner renders early
  incidentsQuery.isLoading &&
  resultsQuery.isLoading &&
  eventsQuery.isLoading;
```

The existing grey `DashboardSkeleton` (rendered when `isInitialLoading` is
true) is exactly the right "neutral" UX while data is in flight — it just
needs to stay visible until **all** queries have resolved, not just the first.

`web/status0` is unaffected: `$org.tsx:14-24` already gates on `isLoading`
before mounting the status view.

## Goal

`OverallStatusBanner` must never render a green, yellow, or red state until
all four dashboard queries have completed their first fetch. The existing
`DashboardSkeleton` covers the interim state — no new component or i18n keys
are needed.

## Out of scope

- A "loading" / grey branch inside `OverallStatusBanner`.
- Playwright test for this fix (manual verification with devtools throttling suffices).
- `web/status0` — already correct.
- Deleting the unused `web/dash0/src/components/shared/status-dashboard.tsx`.

## Implementation

Single file: `web/dash0/src/components/dashboard/dashboard-page.tsx`.

### 1. Fix the loading gate (lines 161-165)

Replace `&&` with `||` and switch to `isPending` (TanStack v5 idiom: true
while there is no cached data yet, regardless of whether a fetch is in
flight):

```ts
const isInitialLoading =
  checksQuery.isPending ||
  incidentsQuery.isPending ||
  resultsQuery.isPending ||
  eventsQuery.isPending;
```

### 2. Align `isEmptyOrg` (line 167)

```ts
const isEmptyOrg = !checksQuery.isPending && checks.length === 0;
```

No other files need changing.

## Verification

```bash
make dev-test   # or make dev
```

- Open `http://localhost:4000/dash0/orgs/default`.
- Devtools → Network → throttle to "Slow 3G". Reload.
- Assert: skeleton stays visible throughout; no green card flash; final banner
  color matches actual check state.
- Repeat with an org that has down checks; confirm red banner appears once
  data lands.

```bash
cd web/dash0 && bun run lint && bun run build
```
