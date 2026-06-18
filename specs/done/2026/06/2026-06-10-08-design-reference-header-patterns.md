# Design reference: canonical list-page and detail-page header patterns

## Context
The design reference page
(`web/dash0/src/routes/orgs/$org/design-reference.tsx`) is the single source of
truth for dash0 UI patterns, but its header guidance has drifted from what the
app actually ships — which is how the inconsistencies in specs
`2026-06-10-02-…` through `-07-…` crept in:

- The **"Page header" section** (lines 388-406) promotes the boxed `PageHeader`
  component ("Use it for new pages; existing inline headers will adopt it over
  time") — but almost every list page instead uses an **inline** `h-7 w-7
  text-muted-foreground` icon + `text-3xl font-bold` title with a right-aligned
  `+ New X` button. The reference documents a pattern the codebase doesn't use,
  so copying from it produced the odd-one-out `me/notifications` header.
- The **"Detail page header" section** (lines 550-576) tells contributors to put
  the back button **on the far left** — but the agreed convention (and what
  status-pages / discovery are being changed to in `-02` and `-05`) is the back
  arrow as the **leftmost item of the right-aligned action cluster**.

## Goal
Update the design reference so both header sections document the **actual
canonical patterns**, with copyable snippets, so future pages match by
construction.

## Behaviour
### A — List-page header (revise the "Page header" section, lines 388-406)
- Document the inline list-page header as canonical:
  - row: `flex items-center justify-between`,
  - left: `<h1 className="text-3xl font-bold tracking-tight flex items-center
    gap-2"><Icon className="h-7 w-7 text-muted-foreground" /> Title</h1>` plus an
    optional `<p className="text-muted-foreground">` subtitle,
  - right: the primary action (`<Button><Plus className="mr-2 h-4 w-4" /> New
    X</Button>`), label visible at all widths.
- Render a live preview of this pattern and a `CodeSnippet` with the markup.
- Clarify the role of the `PageHeader` component: either demote it (note it is
  legacy / used only where a boxed icon is intentional) or remove its promotion
  so the catalog stops steering new pages toward it. Do not delete the component.

### B — Detail-page header (revise the section at lines 550-576)
- Change the copy and the `ExampleRow` preview so the back arrow is the
  **leftmost item of the right-aligned action cluster**, not on the far left:
  - row: `flex items-start justify-between gap-3`,
  - left: title block (`<h1>` + optional subtitle/status), `min-w-0 flex-1`,
  - right: `flex gap-2 shrink-0` cluster whose first child is the icon-only ghost
    back button (`<Button variant="ghost" size="icon" aria-label="Back">
    <ArrowLeft /></Button>`), followed by the page actions (View/Edit/Delete,
    Refresh, etc.).
- Keep the rule that the back button is **always icon-only** with an
  `aria-label`. Update the `importLine`/snippet to the new layout.

### C — Optional follow-up note (in this spec's text, and a one-line note on the page)
- Documentation alone is what drifted before. Note that extracting shared
  `ListPageHeader` and `DetailPageHeader` components — consumed by the reference
  page and the real routes — would enforce these patterns far better than a
  showcase. Recommended, but out of scope for this spec (file as a follow-up).

## Out of scope
- Building the shared header components (follow-up).
- Migrating routes — those are the sibling specs `-02`, `-05`, `-06`, `-07`.
  This spec only changes the reference page.

## Testing
Visual — the design reference is dev-facing; there is no automated assertion on
its content.
- `make dev-test`, open
  `http://localhost:4000/dash0/orgs/default/design-reference`.
- Confirm the "Page header" section now shows the inline list-page header and the
  "Detail page header" section shows the back arrow inside the right action
  cluster, both in light and dark mode, with working copy-to-clipboard snippets.
- Cross-check that the rendered examples match the headers shipped by
  status-pages list, status-updates, the status-pages detail (`-02`), and
  discovery detail (`-05`).

## Implementation Plan
1. Rewrite `PageHeaderSection()` (lines 388-406) to present the inline list-page
   header pattern with a live preview + `CodeSnippet`; adjust the `PageHeader`
   component's described role.
2. Update the detail-page-header `ExampleRow` + copy (lines 550-576) to the
   back-arrow-in-right-cluster layout, including the `importLine` snippet.
3. Add the brief "consider shared header components" note.
4. Verify visually per Testing; `bun run lint` (dash0).
