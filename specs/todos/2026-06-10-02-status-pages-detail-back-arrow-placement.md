# Status pages detail: back arrow on the left of the top-right actions

## Context
The status page detail route
(`web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx`, header
lines 810-928) currently renders the back arrow on the **far left**, grouped
with the title: a `<Link to="/orgs/$org/status-pages">` wrapping a ghost icon
`Button` (lines 813-817) sits inside the left `flex items-start gap-2 min-w-0
flex-1` block, immediately before the `<h1>`. The View / Edit / Delete buttons
live in a separate right-aligned cluster (`<div className="flex gap-2
shrink-0">`, line 843).

The operator-facing convention we are converging on places the back arrow as the
**leftmost item of the right-aligned action cluster** — the same layout the
integrations detail page already uses
(`integrations.$integrationUid.tsx`, back button passed first in `actions`).
This page is one of the two detail pages still on the old "back on the far left"
layout (the other is discovery — see `2026-06-10-05-…`).

## Goal
Move the back arrow out of the title group and make it the first button in the
top-right action cluster, so the header reads: **title (left) · `[← back] [View]
[Edit] [Delete]` (right)**.

## Behaviour
- Remove the back-arrow `Link`+`Button` from the left title group (lines
  813-817). The left block becomes just the title `<div className="min-w-0
  flex-1">` (name, status dots, slug/description), still filling the row.
- Add the back arrow as the **first** child of the right cluster (`flex gap-2
  shrink-0`, line 843), before the View button. Keep it icon-only, ghost,
  `size="icon"`, wrapping the existing `<Link to="/orgs/$org/status-pages"
  params={{ org }}>`, and add an `aria-label` (reuse the existing
  `statusPages:backToList`/equivalent i18n key, or add one if absent).
- The outer header wrapper stays `flex items-start gap-3 justify-between`; the
  title group keeps `flex-1` so the cluster stays right-aligned.
- View / Edit / Delete buttons, their tooltips, responsive `hidden sm:inline`
  labels, and the delete `AlertDialog` are unchanged.

## Out of scope
- No change to the sections list, add-section dialog, or any non-header markup.
- No change to View/Edit/Delete behaviour or to the page's data fetching.
- `web/dash` (legacy) untouched.

## Testing
dash0 has Playwright E2E only (`web/dash0/e2e/`); status-page coverage lives in
`e2e/status-pages.spec.ts`.
- Assert the back control still navigates to `/dash0/orgs/$org/status-pages`.
- Assert it now renders inside the action cluster, left of the View button
  (e.g. both share the right-aligned row; back arrow precedes View in DOM order).
- Manual: `make dev-test`, open a status page detail, verify desktop + mobile
  (cluster wraps cleanly, touch targets comfortable) and light + dark mode.

## Implementation Plan
1. Edit the header in `status-pages.$statusPageUid.index.tsx`: delete the back
   `Link`/`Button` from the left group; insert it as the first item of the right
   `flex gap-2 shrink-0` cluster with `aria-label`.
2. Add/verify the back-button `aria-label` i18n key in all locales.
3. Update/extend `e2e/status-pages.spec.ts` per Testing.
4. Verify: `bun run lint` (dash0), `make test-dash`, manual mobile + dark check.
