# Check detail header: drop the action toolbar onto its own row so a long name stops squeezing it

## Context

On a check detail page — e.g.
`https://solidping.k8xp.com/dash0/orgs/test/checks/561d0170-fda3-44a6-9fe9-d66d99fc998c?graphPeriod=hour`
— the header puts the title block on the **left** and a wide action cluster on the
**right** of the same row. The title (the check name) eats the horizontal space and
pushes the buttons hard against the right edge. With a long name (the E2E
`E2E Overflow … Very Long Check Name …` checks are the worst case) the row feels cramped
even on a wide desktop, where each button is showing its **text label**.

The header today is a single `flex items-start justify-between gap-3` row
([`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:449-635`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx)):

- Left: status dot + `h1` (truncates) + type/pending badges + slug/uid sub-links, wrapped
  in `min-w-0 flex-1`.
- Right: a `flex shrink-0 items-center gap-2` cluster — back arrow, then a 6-button toolbar
  (Edit, Enable/Disable, Clone, Badges, Refresh, Delete). Per the most recent change
  ([`2026-06-15-07-check-detail-action-buttons-never-collapse.md`](../done/2026/06/2026-06-15-07-check-detail-action-buttons-never-collapse.md))
  the buttons are **always visible**, icon-only below `lg` and **icon + label at `lg+`**.

The detail page is uniquely action-dense: seven controls (back + six actions). The design
reference's canonical detail header
([`design-reference.tsx:660-717`](../../web/dash0/src/routes/orgs/$org/design-reference.tsx))
assumes ~three actions sitting beside the title — that assumption breaks here, which is why
the title and the toolbar fight for the same row.

## Decision

The user asked for the buttons to be **"lower or smaller — your choice."** This spec takes
**lower**: move the whole action toolbar onto **its own full-width row beneath the title
block**. Rationale over the "smaller / icon-only everywhere" option:

- The title gets the entire first row and the toolbar gets the entire second row, so they
  never compete — robust to *any* check-name length, not just today's worst case.
- It **keeps the text labels** that were deliberately added for discoverability
  ([spec 04](../done/2026/06/2026-06-15-04-check-detail-badges-button-and-labeled-actions.md)),
  rather than throwing them away to win horizontal space.
- It's a pure layout change — the per-button responsive behaviour and the delete dialog
  wiring are untouched, so there's nothing to regress.

## Goals

- The title block (status dot, `h1`, type/pending badges, slug/uid sub-links) occupies its
  own row; the `h1` still truncates and never causes horizontal overflow.
- The action toolbar (back arrow + Edit, Enable/Disable, Clone, Badges, Refresh, Delete)
  sits on a **second row below** the title, right-aligned, and is no longer squeezed by the
  name — at desktop width every button shows its label comfortably.
- All existing button behaviour is preserved: back arrow icon-only ghost first; buttons
  icon-only below `lg`, icon + label at `lg+`; Delete stays `variant="destructive"` +
  `Trash2`; the controlled delete `AlertDialog` keeps working.
- No horizontal overflow at any width; on a narrow phone the toolbar may wrap across lines
  rather than overflow.

## Out of scope

- Adopting the "smaller / icon-only at all widths" alternative — not chosen.
- Re-collapsing actions into a `MoreVertical` overflow menu (that strategy was removed in
  spec 07; don't reintroduce it).
- Touching any other page's header, or the shared `PageHeader` component.
- Changing the toolbar's per-button responsive breakpoints, labels, or icons.
- New Tailwind breakpoint tokens or container queries.

## Implementation

All changes are in
[`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx),
purely to the outer header layout at lines 449-635. **Do not** alter the inner toolbar
`<div className="flex items-center gap-2">` (550-635) or any individual `<Button>` — only
restructure the two wrappers around them.

### 1. Make the header stack vertically (line 449)

Change the outer row from a horizontal `justify-between` row to a vertical stack:

```tsx
// before
<div className="flex items-start justify-between gap-3">
// after
<div className="flex flex-col gap-3">
```

### 2. Keep the title block as the first row (lines 450-536)

Leave the title block essentially as-is. In a flex column its child stretches full width, so
`min-w-0 flex-1` is harmless and still lets the `h1`'s `truncate` work — keep it. No content
changes to the dot / title / badges / slug-uid sub-links.

### 3. Put the action cluster on its own row, right-aligned (line 537)

Change the action-cluster wrapper so it spans the full width and pushes its contents to the
right, wrapping instead of overflowing on very narrow screens:

```tsx
// before
<div className="flex shrink-0 items-center gap-2">
// after
<div className="flex flex-wrap items-center justify-end gap-2">
```

Everything inside this wrapper — the back `Button` (538-547), the inline toolbar `div`
(550-635), and the triggerless delete `AlertDialog` (637+) — stays exactly as written.

### 4. Nothing else

The back arrow stays icon-only ghost and leads the cluster (matching the design-reference
"back arrow leads the right-aligned cluster" rule). Button labels still appear at `lg+`;
icon-only below `lg`. The delete confirm flow is unchanged.

## Design reference

The detail-header section of the design reference
([`design-reference.tsx:660-717`](../../web/dash0/src/routes/orgs/$org/design-reference.tsx))
documents only the single-row "title block + right-aligned action cluster" variant. Add a
short third variant after it (around line 717) documenting the **stacked** form for
action-dense detail headers: title block on its own row, then a
`flex flex-wrap items-center justify-end gap-2` action row beneath it, used when the page
carries more than ~three actions that would otherwise crowd a long title. Mirror the existing
`ExampleRow` pattern (preview + `importLine`) so the catalog stays the single source of truth
per CLAUDE.md.

## Verification

1. Open the reported check
   (`…/checks/561d0170-fda3-44a6-9fe9-d66d99fc998c?graphPeriod=hour`) on a wide desktop:
   the check name sits alone on the first row (truncating if very long) and the labeled
   action toolbar sits on a second row below it, no longer pinched against the name. No
   horizontal scrollbar.
2. At a tablet width (~768 px): title row + icon-only toolbar row; no overflow.
3. At a phone width (~375 px): the toolbar wraps within its row if needed; all actions remain
   reachable (no hidden overflow menu). The name truncates.
4. Back, Edit, Enable/Disable, Clone, Badges, Refresh all work; Delete opens the confirm
   dialog and removes the check.
5. `bun run lint`, `bun run build` (tsc included) pass in `web/dash0`.

## Tests

Extend the check-detail Playwright coverage in `web/dash0/e2e/` (the existing
`check-detail.spec.ts` already exercises the header):

- For a long-named check at a desktop viewport, assert the header does **not** overflow
  horizontally (e.g. `scrollWidth <= clientWidth` on the page container) and that the action
  buttons render below the title block (compare bounding-box `y` of the title vs. a toolbar
  button — the button's top is greater than the title's bottom).
- Assert all six actions plus the back arrow are present and the labels are visible at a
  wide viewport.

## Files referenced

- `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:449-635` — the header (the change).
- `web/dash0/src/routes/orgs/$org/design-reference.tsx:660-717` — canonical detail-header
  patterns; add the stacked variant.
- `specs/done/2026/06/2026-06-15-07-check-detail-action-buttons-never-collapse.md` — the
  current never-collapse button behaviour this builds on.
- `web/dash0/e2e/check-detail.spec.ts` — header E2E coverage to extend.

## Implementation Plan

1. Outer wrapper `flex items-start justify-between gap-3` → `flex flex-col gap-3` (line 449).
2. Action-cluster wrapper `flex shrink-0 items-center gap-2` →
   `flex flex-wrap items-center justify-end gap-2` (line 537). Leave the inner toolbar,
   every `<Button>`, and the delete `AlertDialog` untouched.
3. Add the stacked detail-header variant to the design reference (ExampleRow + importLine).
4. Extend `check-detail.spec.ts` with the no-overflow / toolbar-below-title assertions.
5. QA: `bun run lint` + `bun run build` in `web/dash0`; visually confirm at desktop, tablet,
   and phone widths.
