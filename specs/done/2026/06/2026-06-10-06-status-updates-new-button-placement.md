# Status updates: align the "New update" button placement with other list pages

## Context
The status-updates list route
(`web/dash0/src/routes/orgs/$org/status-updates.index.tsx`, header lines
200-223) places its "New update" button differently from sibling list pages. Its
header wrapper is `flex items-start justify-between gap-4`, and the button label
is hidden on mobile (`<span className="hidden sm:inline">New update</span>`).

The status-pages list (`status-pages.index.tsx`, lines 168-184) — which the user
pointed to as the reference — uses `flex items-center justify-between`, and its
primary action shows its label at all widths (`<Button><Plus … />New status
page</Button>`). Because status-updates uses `items-start`, its button hugs the
top of the row instead of being vertically centered against the title block, so
the two pages don't line up.

## Goal
Make the status-updates header match the status-pages list pattern so the
primary action button sits in the same place across list pages.

## Behaviour
- Change the header wrapper from `flex items-start justify-between gap-4` to
  `flex items-center justify-between` (drop `gap-4`, or keep a small gap if the
  title block needs breathing room — match status-pages, which has none), so the
  "New update" button is vertically centered with the title + subtitle block.
- Show the "New update" label at all widths to match the always-visible "New
  status page" label: remove the `hidden sm:inline` wrapper around the text (keep
  the `Plus` icon with `mr-2`).
- Keep `data-testid="status-updates-new"`, the `Megaphone` title icon, and the
  link target `/orgs/$org/status-updates/new`.

## Out of scope
- No change to the status-updates list/table, filters, or data fetching.
- No change to the status-pages list page (it is the reference, already correct).
- The canonical list-page header is documented separately in
  `2026-06-10-08-design-reference-header-patterns.md`.

## Testing
dash0 Playwright E2E (`web/dash0/e2e/`); status-updates coverage in
`e2e/status-updates.spec.ts`.
- The "New update" button (`status-updates-new`) is present, label visible, and
  navigates to `/dash0/orgs/$org/status-updates/new`.
- Manual: `make dev-test`, open `/status-updates` and `/status-pages`
  side-by-side, confirm the primary action sits in the same spot; desktop +
  mobile, light + dark.

## Implementation Plan
1. Edit the header wrapper in `status-updates.index.tsx` to `flex items-center
   justify-between` (matching `status-pages.index.tsx:168`).
2. Remove the `hidden sm:inline` wrapper so "New update" is always visible.
3. Update `e2e/status-updates.spec.ts` if it asserted the mobile-hidden label.
4. Verify: `bun run lint` (dash0), `make test-dash`, manual side-by-side check.
