# Status updates list: lead with a clickable title and add a direct public link

## Context

The operator status-updates list lives at
`http://localhost:4000/dash0/orgs/default/status-updates`
([`web/dash0/src/routes/orgs/$org/status-updates.index.tsx`](../../web/dash0/src/routes/orgs/$org/status-updates.index.tsx)).
It renders a table with four columns — **Kind** (badge) · **Title** · **Date** ·
**actions** — where each row's actions are two ghost icon buttons: `Pencil` (links to the
edit route) and `Trash2` (opens a delete `AlertDialog`). The table header is at
[`status-updates.index.tsx:331-338`](../../web/dash0/src/routes/orgs/$org/status-updates.index.tsx)
and the row component (`StatusUpdateRow`) at
[`status-updates.index.tsx:75-128`](../../web/dash0/src/routes/orgs/$org/status-updates.index.tsx).

Today:

- **Kind comes first**, so the eye lands on a coloured badge before the thing that actually
  identifies the row — the title. The title is plain, non-interactive text
  (`<span className="font-medium">{update.title}</span>`,
  [`status-updates.index.tsx:90`](../../web/dash0/src/routes/orgs/$org/status-updates.index.tsx)).
- The only way to open an update is the `Pencil` icon at the far right of the row.
- There is **no way to jump to the public-facing rendering** of an update. Each update belongs
  to a status page (`StatusUpdate.statusPageUid`,
  [`web/dash0/src/api/hooks.ts:3077`](../../web/dash0/src/api/hooks.ts)) whose public view is
  served at `/status0/{org}/{slug}` — the same target the status-pages list already opens via
  an `ExternalLink` ghost button
  ([`status-pages.index.tsx:91-98`](../../web/dash0/src/routes/orgs/$org/status-pages.index.tsx)).

The user asked, for this page, to:

1. **Start with the title** — make it the first column.
2. **Make the title clickable to allow edit.**
3. **Add an external link for direct access to the update** on its public status page.

## Decision

Rework `StatusUpdateRow` (and the table header) only:

1. **Title first.** Reorder columns to **Title · Kind · Date · actions**. The `Title`
   `TableHead` moves to the first position; `Kind` follows.

2. **Title is the edit affordance.** Render the title as a TanStack `<Link>` to the existing
   edit route `/orgs/$org/status-updates/$updateUid/edit` (the same target the `Pencil`
   already uses), styled as a normal text link (`font-medium`, `hover:underline`,
   inherits `text-foreground`), with `data-testid="status-update-row-title"`.

3. **External link to the public update.** Add an `ExternalLink` ghost icon button as the
   **first** row action (left of `Pencil`/`Trash2`), mirroring the status-pages pattern
   exactly: `<a href={publicUrl} target="_blank" rel="noopener noreferrer">`. The target is
   the update's public status page, deep-linked to the update itself:
   `/status0/{org}/{slug}#update-{update.uid}`.

   - The **slug** is resolved from `update.statusPageUid` against the `pages` list already
     loaded on this page (`useStatusPages(org)`, [`status-updates.index.tsx:139`](../../web/dash0/src/routes/orgs/$org/status-updates.index.tsx)) —
     build a `Map<statusPageUid, slug>` once in the page component and pass the resolved
     `slug` into each row. **No new API call.**
   - If the slug can't be resolved (page deleted / not yet loaded), **omit** the external-link
     button for that row rather than rendering a broken `/status0/{org}/undefined` link.

   To make the `#update-{uid}` anchor actually land on the update, add a matching DOM id on
   the public card (see Implementation step 4). This deep-link **gracefully degrades**: the
   public page only shows updates inside its `historyDays` window
   ([`server/internal/handlers/statuspages/service.go:827-848`](../../server/internal/handlers/statuspages/service.go)),
   so for an older update the anchor simply won't match and the browser stays at the top of
   the page — still "direct access" to where the update lives.

**Keep both the `Pencil` and the title-link as edit entry points.** The title-link is the
obvious, discoverable affordance the user asked for; the `Pencil`/`Trash2` pairing is the
repo-wide row-actions convention (`web/dash0/CLAUDE.md` → "Row actions: icons, not menus").
Keeping `Pencil` keeps every list table consistent and is a one-line cost. Dropping it as
redundant is a reasonable alternative (see Out of scope) but is **not** taken here.

## Goals

- The **Title** is the first column and is a link that opens the update's edit page.
- A new **ExternalLink** ghost icon button opens the update's public status page
  (`/status0/{org}/{slug}#update-{uid}`) in a new tab, with `rel="noopener noreferrer"`.
- The external link is omitted when the parent page's slug can't be resolved (no broken URL).
- Visiting `…/status0/{org}/{slug}#update-{uid}` scrolls to that update when it is within the
  page's history window; otherwise the page loads normally at the top.
- Existing behaviour is preserved: `Pencil` → edit, `Trash2` → delete dialog, all filters,
  search, refresh, and the empty/loading states are untouched.
- The page remains fully usable on mobile (the action cluster already lives in a
  `flex items-center justify-end gap-1` container — the extra icon wraps/fits within it).

## Out of scope

- **Dropping the `Pencil` edit icon** as redundant with the clickable title. Plausible, but
  not done here to stay consistent with every other list table; revisit only if the owner
  asks.
- A dedicated read-only detail view for a status update (none exists — the layout route
  [`status-updates.$updateUid.tsx`](../../web/dash0/src/routes/orgs/$org/status-updates.$updateUid.tsx)
  is just an `<Outlet>`; edit is the canonical open target).
- Changing what the public status page shows, its `historyDays` window, or adding
  pagination so older updates become deep-linkable.
- The author-supplied `update.linkUrl` field (a link the author attaches to an update) — that
  is a different concept and is left as-is.
- Any change to the `web/dash` (legacy) dashboard.

## Implementation

1. **Imports / header.** `ExternalLink` is already imported in the status-pages list; add it
   to the `lucide-react` import in
   [`status-updates.index.tsx:3`](../../web/dash0/src/routes/orgs/$org/status-updates.index.tsx).
   In the `TableHeader` ([`status-updates.index.tsx:331-338`](../../web/dash0/src/routes/orgs/$org/status-updates.index.tsx))
   reorder the `TableHead`s to `Title`, `Kind`, `Date`, then the empty actions head.

2. **Resolve slugs once.** In `StatusUpdatesIndexPage`, derive
   `const pageSlugByUid = useMemo(() => new Map((pages ?? []).map((p) => [p.uid, p.slug])), [pages])`
   and pass `publicSlug={pageSlugByUid.get(u.statusPageUid)}` into each `StatusUpdateRow`.

3. **`StatusUpdateRow` changes** ([`status-updates.index.tsx:75-128`](../../web/dash0/src/routes/orgs/$org/status-updates.index.tsx)):
   - Add `publicSlug?: string` to its props.
   - **Cell order:** emit the **Title** cell first, as a `<Link to="/orgs/$org/status-updates/$updateUid/edit" params={{ org, updateUid: update.uid }}>` styled
     `font-medium hover:underline`, `data-testid="status-update-row-title"`. Then the
     **Kind** badge cell, then the **Date** cell (unchanged), then the actions cell.
   - **Actions cell:** prepend an `ExternalLink` ghost icon button when `publicSlug` is set:
     ```tsx
     {publicSlug && (
       <Button asChild variant="ghost" size="icon" className="h-8 w-8"
               aria-label="View public update" title="View public update">
         <a href={`/status0/${org}/${publicSlug}#update-${update.uid}`}
            target="_blank" rel="noopener noreferrer"
            data-testid="status-update-row-view">
           <ExternalLink className="h-4 w-4" />
         </a>
       </Button>
     )}
     ```
     Keep the existing `Pencil` edit link and `Trash2` delete button after it, unchanged
     (`data-testid` `status-update-row-edit` / `status-update-row-delete` stay valid).

4. **Public anchor target.** In
   [`web/status0/src/components/shared/status-update-card.tsx`](../../web/status0/src/components/shared/status-update-card.tsx),
   add `id={`update-${update.uid}`}` and `scroll-mt-24` to the card's root `<div>`
   ([`status-update-card.tsx:85`](../../web/status0/src/components/shared/status-update-card.tsx))
   so a `#update-{uid}` hash resolves and isn't hidden under the page header.

5. **Scroll on async load.** Because `recentUpdates` arrive after first paint, a native hash
   jump on initial load can miss. In the public status-page route
   ([`web/status0/src/routes/$org.$slug.tsx`](../../web/status0/src/routes/$org.$slug.tsx))
   add a `useEffect` that, once `page` is loaded, reads `window.location.hash`, and if it
   matches `#update-…` and the element exists, calls
   `el.scrollIntoView({ behavior: "smooth", block: "start" })`. Guard against the element
   being absent (out-of-window update) — do nothing in that case.

## Design reference

No new primitive. This reuses the canonical row-action shape already cataloged in
[`web/dash0/src/routes/orgs/$org/design-reference.tsx`](../../web/dash0/src/routes/orgs/$org/design-reference.tsx)
(ghost `size="icon"` action buttons; `Pencil`/`Trash2` row actions; clickable resource name
as a `<Link>`) and the `ExternalLink`-to-public pattern from
[`status-pages.index.tsx:91-98`](../../web/dash0/src/routes/orgs/$org/status-pages.index.tsx).
No catalog change required.

## Verification

With `make dev-test` running, at `/dash0/orgs/{org}/status-updates`:

1. The first column is the **Title**; **Kind** is second, **Date** third.
2. Clicking a title navigates to `/orgs/{org}/status-updates/{uid}/edit`.
3. The `Pencil` icon still navigates to the same edit page; `Trash2` still opens the delete
   dialog and deleting still works.
4. The new `ExternalLink` icon opens `/status0/{org}/{slug}#update-{uid}` in a new tab.
5. For a recent update, that public page scrolls to the update card; for an old update
   (outside `historyDays`) it loads at the top without error.
6. Mobile width: the row is usable and the action icons don't overflow.

## Tests

Extend [`web/dash0/e2e/status-updates.spec.ts`](../../web/dash0/e2e/status-updates.spec.ts)
(reuse its existing `status-update-row*` testids):

- Title link: click `status-update-row-title` → assert URL matches the edit route and the
  edit form (`status-update-form-title`) is visible.
- External link: assert the `status-update-row-view` anchor has
  `target="_blank"`, `rel` containing `noopener`, and an `href` ending
  `/status0/{org}/{slug}#update-{uid}` (don't navigate cross-app; assert the attributes).
- Regression: `status-update-row-edit` and `status-update-row-delete` still work
  (the existing create/edit/delete flows must keep passing).

Run with `make test-dash`.

## Files referenced

- [`web/dash0/src/routes/orgs/$org/status-updates.index.tsx`](../../web/dash0/src/routes/orgs/$org/status-updates.index.tsx) — list page + `StatusUpdateRow` (primary change)
- [`web/dash0/src/api/hooks.ts`](../../web/dash0/src/api/hooks.ts) — `StatusUpdate` type (`statusPageUid`, `uid`), `useStatusPages`
- [`web/dash0/src/routes/orgs/$org/status-pages.index.tsx`](../../web/dash0/src/routes/orgs/$org/status-pages.index.tsx) — canonical `ExternalLink`-to-`/status0/{org}/{slug}` pattern
- [`web/status0/src/components/shared/status-update-card.tsx`](../../web/status0/src/components/shared/status-update-card.tsx) — public card; add anchor `id`
- [`web/status0/src/routes/$org.$slug.tsx`](../../web/status0/src/routes/$org.$slug.tsx) — public page; add scroll-to-hash effect
- [`server/internal/handlers/statuspages/service.go`](../../server/internal/handlers/statuspages/service.go) — `historyDays` window for `recentUpdates` (context for the deep-link caveat)
- [`web/dash0/e2e/status-updates.spec.ts`](../../web/dash0/e2e/status-updates.spec.ts) — E2E to extend

## Implementation Plan

Confirmed against the current tree: the page is in its pre-spec state (Kind
first, plain non-link title, no external link). The dash0 `status-updates.index.tsx`
file uses **hardcoded English** strings (e.g. `aria-label="Edit"`), not i18n — so
new copy here stays hardcoded to match the file's own convention (the spec's code
samples already do this). No backend change is needed; all five steps are frontend.

1. **dash0 list — imports + header** (`web/dash0/src/routes/orgs/$org/status-updates.index.tsx`):
   - Add `ExternalLink` to the `lucide-react` import (line 3).
   - In `TableHeader` (lines 331-338) reorder heads to **Title · Kind · Date · actions**.

2. **dash0 list — resolve slugs once** (`StatusUpdatesIndexPage`):
   - `const pageSlugByUid = useMemo(() => new Map((pages ?? []).map((p) => [p.uid, p.slug] as const)), [pages]);`
   - Import `useMemo` from `react`.
   - Pass `publicSlug={pageSlugByUid.get(u.statusPageUid)}` into each `StatusUpdateRow`.

3. **dash0 list — `StatusUpdateRow`** (lines 75-128):
   - Add `publicSlug?: string` prop.
   - Emit **Title** cell first, as a TanStack `<Link to="/orgs/$org/status-updates/$updateUid/edit" params={{ org, updateUid: update.uid }}>` styled `font-medium hover:underline` with `data-testid="status-update-row-title"`. Then **Kind** badge cell, then **Date** cell (unchanged), then actions cell.
   - In the actions cell, **prepend** an `ExternalLink` ghost icon `Button asChild` (only when `publicSlug` is set) wrapping `<a href={`/status0/${org}/${publicSlug}#update-${update.uid}`} target="_blank" rel="noopener noreferrer" data-testid="status-update-row-view">`, `aria-label`/`title="View public update"`. Keep `Pencil` (`status-update-row-edit`) and `Trash2` (`status-update-row-delete`) after it, unchanged.

4. **status0 public card — anchor target** (`web/status0/src/components/shared/status-update-card.tsx`, root `<div>` line 85): add `id={`update-${update.uid}`}` and `scroll-mt-24`.

5. **status0 public route — scroll on async load** (`web/status0/src/routes/$org.$slug.tsx`): add a `useEffect` that, once `page` is loaded, reads `window.location.hash`; if it matches `#update-…` and the element exists, `el.scrollIntoView({ behavior: "smooth", block: "start" })`. No-op when the element is absent (out-of-window update).

6. **E2E** (`web/dash0/e2e/status-updates.spec.ts`): extend the existing flow to
   - assert column order (Title before Kind in the header),
   - click `status-update-row-title` → lands on the edit route, `status-update-form-title` visible,
   - assert the `status-update-row-view` anchor has `target="_blank"`, `rel` containing `noopener`, and `href` ending `/status0/{org}/{slug}#update-{uid}` (no cross-app nav),
   - keep the existing `status-update-row-edit` / `status-update-row-delete` regression coverage.

7. **QA**: `make build-dash0` (type-checks + builds dash0) and `make build-status0` (type-checks + builds status0). Note `make lint-dash` targets the legacy `web/dash`, so run the dash0 eslint directly (`cd web/dash0 && bun run lint`) and prove zero NEW errors vs. the RED baseline. No backend touched → no Go QA. i18n: no keys added (page is hardcoded-English by convention), so no locale parity work.
