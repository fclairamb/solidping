# Status pages list: replace row dropdown with three icon buttons

## Context

The status-pages list
(`web/dash0/src/routes/orgs/$org/status-pages.index.tsx`) hides every
row action behind a `MoreVertical` dropdown
(`StatusPageRow`, lines 86-117) with three items:

- "View Details" → `/orgs/$org/status-pages/$uid` (the detail /
  management page)
- "Edit" → `/orgs/$org/status-pages/$uid/edit` (the form-only metadata
  page)
- "Delete" → opens an `AlertDialog`

Two real problems with this:

1. **There's no row-level affordance for the public status page.**
   To reach the public view (`/status0/{org}/{slug}`) you must first
   open the detail page, then click "View public" in its header
   (`status-pages.$statusPageUid.index.tsx:695-717`). For something
   designed to be shared externally that's a buried action.
2. **The dropdown contradicts dash0 row-action conventions.**
   `web/dash0/CLAUDE.md` (Row actions: icons, not menus): "prefer two
   ghost icon buttons (Pencil for edit, Trash2 for delete, with a
   text-destructive class on the latter) over a DropdownMenu with a
   MoreVertical trigger". The status-pages list is the last list page
   still using the dropdown pattern.

The dropdown's "View Details" + "Edit" split is also confusing: today
the page name (col 0) already navigates to the detail page on click
(lines 63-70), so "View Details" is just a duplicate of clicking the
title. The form-only edit page is an internal sub-action, not the
primary entry; it shouldn't be the destination of a top-level "Edit"
button.

## Goal

On every row, three ghost icon buttons in this order:

1. **View** (external-link icon) — opens
   `/status0/{org}/{slug}` in a new tab. The public status page.
2. **Edit** (pencil icon) — navigates to
   `/orgs/$org/status-pages/$uid` (the detail / management page).
   Same destination as clicking the title text.
3. **Delete** (trash icon, `text-destructive`) — opens the existing
   confirmation dialog.

The form-only metadata page (`.../$uid/edit`) stays reachable from the
detail page header (`Pencil` button at
`status-pages.$statusPageUid.index.tsx:718-737`), where it logically
belongs as a sub-action of "manage this status page".

## Approach

In `status-pages.index.tsx`:

1. **Remove the dropdown** (lines 86-117) and its imports
   (`DropdownMenu*`, `MoreVertical`).
2. **Render three icon buttons** in the action cell, all
   `variant="ghost" size="icon"`:

   ```tsx
   <a
     href={`/status0/${org}/${page.slug}`}
     target="_blank"
     rel="noopener noreferrer"
     aria-label={t("statusPages:viewPublic")}
   >
     <Button variant="ghost" size="icon" className="h-8 w-8">
       <ExternalLink className="h-4 w-4" />
     </Button>
   </a>
   <Link to="/orgs/$org/status-pages/$statusPageUid"
         params={{ org, statusPageUid: page.uid }}
         aria-label={t("statusPages:edit")}>
     <Button variant="ghost" size="icon" className="h-8 w-8">
       <Pencil className="h-4 w-4" />
     </Button>
   </Link>
   <Button
     variant="ghost"
     size="icon"
     className="h-8 w-8 text-destructive"
     onClick={() => onDelete(page.uid)}
     aria-label={t("statusPages:delete")}
   >
     <Trash2 className="h-4 w-4" />
   </Button>
   ```

   Wrap each button in a `Tooltip` showing its label, matching the
   detail-page header buttons' tooltip pattern at
   `status-pages.$statusPageUid.index.tsx:695-737`.

3. **Title click**: leave as-is — the title `<Link>` (lines 63-70)
   already navigates to the detail page. The user explicitly called
   out "(or click on the text)"; that's already the behavior.
4. **Disabled-state nuance**: if `page.enabled === false`, the public
   view will 404 / show a disabled banner. Keep the View button
   enabled — operators do legitimately want to preview a disabled
   page. (No-op for this spec; just don't disable it.)
5. **Translations**: ensure
   `statusPages.viewPublic` / `statusPages.edit` / `statusPages.delete`
   exist in `web/dash0/src/locales/{en,de,es,fr}/statusPages.json`.
   Some of these keys already exist (`viewDetails`, `edit`, `delete`);
   add the missing ones and consider deprecating `viewDetails` (no
   longer referenced after this change — sweep when convenient).

## Files to change

- `web/dash0/src/routes/orgs/$org/status-pages.index.tsx` — replace
  dropdown with three icon buttons; trim imports
- `web/dash0/src/locales/{en,de,es,fr}/statusPages.json` — add
  `viewPublic` (label and `aria-label`-friendly tooltip)

## Verification

1. `make dev`, open `/dash0/orgs/default/status-pages` with ≥1 status
   page.
2. Each row shows three icon buttons; no dropdown trigger; no
   `MoreVertical` icon.
3. Hovering each button shows a tooltip ("View public" / "Edit" /
   "Delete").
4. View opens `/status0/{org}/{slug}` in a new tab.
5. Edit navigates to the detail page (same destination as clicking
   the page name).
6. Delete opens the existing confirmation dialog; confirming removes
   the row.
7. Keyboard navigation: Tab order through a row hits View → Edit →
   Delete in that visual order; Enter activates each.
8. Smoke-test the existing Playwright suite under `web/dash0/e2e/`
   for any selectors that used the dropdown trigger
   (`role="menu"` etc.) and update them.

## Implementation Plan

1. **Add `viewPublic` key** to `web/dash0/src/locales/{en,de,es,fr}/statusPages.json`
   alongside the existing `viewDetails` / `edit` / `delete`.
2. **Replace dropdown with three icon buttons** in
   `web/dash0/src/routes/orgs/$org/status-pages.index.tsx`'s `StatusPageRow`:
   external-link → public page in new tab; pencil → detail page;
   trash → existing AlertDialog flow. Drop the `DropdownMenu*` and
   `MoreVertical` imports.
3. **Tooltips** on each button reusing the existing tooltip pattern
   from the detail-page header (no need to add a tooltip lib —
   `aria-label` + `title` is enough for v1).
4. **No e2e change**: a quick grep for `MoreVertical` / `role="menu"`
   / dropdown selectors against the status-pages list confirms no
   existing test depends on the dropdown.
5. **QA**: `make build-backend build-client lint-back test`.
6. **Audit + archive**.
