# Discovery detail: back arrow in the right cluster + labelled refresh on desktop

## Context
The discovery scan detail route
(`web/dash0/src/routes/orgs/$org/discovery.$jobUid.index.tsx`, header lines
176-217) currently renders the back arrow on the **far left**, grouped with the
title (`<Button asChild variant="ghost" size="icon">` wrapping `<Link
to="/orgs/$org/discovery">`, lines 179-183, inside the left `flex items-center
gap-3` block). The right-aligned cluster (line 196) holds the optional Stop
button and an **icon-only** Refresh button (`RefreshCw`, `size="icon"`, lines
208-215).

Two issues:
1. Like the status-pages detail page (`2026-06-10-02-…`), the back arrow should
   sit as the leftmost item of the right-aligned action cluster, not on the far
   left.
2. The Refresh button is icon-only even on wide screens, where there is plenty
   of room for a label — inconsistent with the labelled-on-desktop button idiom
   used elsewhere (e.g. status-pages detail View/Edit/Delete, the design
   reference's Reload example).

## Goal
- Move the back arrow into the right action cluster as its leftmost item.
- Make Refresh a **full labelled button on desktop** (icon + "Refresh"),
  icon-only on mobile, preserving the spinning state.

## Behaviour
### A — Back arrow placement
- Remove the back-arrow `Button`/`Link` from the left title group (lines
  179-183). The left block becomes just the title + status badge.
- Insert it as the **first** child of the right cluster (`flex items-center
  gap-2`, line 196), before the Stop button, keeping it ghost `size="icon"`
  wrapping `<Link to="/orgs/$org/discovery" params={{ org }}>`, with an
  `aria-label` (reuse/add a `discovery` back i18n key).
- The header wrapper stays `flex flex-wrap items-start justify-between gap-4`.

### B — Labelled refresh on desktop
- The Refresh button changes from `size="icon"` to the default size with the
  responsive label idiom:
  ```tsx
  <Button variant="outline" onClick={…} disabled={isLoading} aria-label={t("refresh")}>
    <RefreshCw className={`h-4 w-4 sm:mr-2 ${isRefreshing ? "animate-spin" : ""}`} />
    <span className="hidden sm:inline">{t("refresh")}</span>
  </Button>
  ```
- Keep the spin-while-refreshing animation, the `disabled={isLoading}` guard, and
  both `void refetchScan()` / `void refetchHosts()` calls.
- Add a `refresh` i18n key (discovery namespace) in all locales if not present.

## Out of scope
- No change to the Stop-scan button behaviour or the stop confirmation dialog.
- No change to scan progress, host table, or data fetching.
- `web/dash` (legacy) untouched.

## Testing
dash0 Playwright E2E (`web/dash0/e2e/`); discovery coverage in
`e2e/discovery.spec.ts` (create/extend as needed).
- Back arrow renders in the right cluster (leftmost) and navigates to
  `/dash0/orgs/$org/discovery`.
- At desktop viewport width the Refresh button shows the "Refresh" label; at
  mobile width it is icon-only.
- Manual: `make dev-test`, open a scan detail, resize across the `sm` breakpoint,
  trigger a refresh (icon spins), light + dark.

## Implementation Plan
1. Edit the header in `discovery.$jobUid.index.tsx`: remove the left back button;
   add it as the first item of the right cluster with `aria-label`.
2. Convert the Refresh button to the labelled-on-desktop idiom, preserving spin
   state and refetch handlers.
3. Add the `refresh` (and back-button) i18n keys in all locales.
4. Add/extend `e2e/discovery.spec.ts` per Testing.
5. Verify: `bun run lint` (dash0), `make test-dash`, manual breakpoint + dark check.
