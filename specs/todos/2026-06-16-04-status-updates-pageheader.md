# Status Updates list: migrate the header to the canonical PageHeader

## Context

Spec `2026-06-16-03-standardize-page-headers-on-pageheader` (now in
`specs/done/2026/06/`) made the boxed **`PageHeader`** component
([`web/dash0/src/components/shared/page-header.tsx`](../../web/dash0/src/components/shared/page-header.tsx))
the single canonical page header — an icon inside a rounded muted tile
(`h-10 w-10 rounded-md bg-muted`), a `text-2xl font-semibold tracking-tight`
title, an optional muted subtitle, and right-aligned `actions`. It rewrote the
design reference to retire the old inline `text-3xl` header and migrated 11 list
pages (Dashboard, Checks, Incidents, Status Pages, On-call, …).

Its migration table **missed the Status Updates list page.** As shipped,
[`status-updates.index.tsx:203-223`](../../web/dash0/src/routes/orgs/$org/status-updates.index.tsx)
still hand-rolls the retired inline header:

```tsx
<div className="flex items-center justify-between">
  <div>
    <h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
      <Megaphone className="h-7 w-7 text-muted-foreground" />
      Status updates
    </h1>
    <p className="text-muted-foreground">
      Publish narrative updates on your status pages.
    </p>
  </div>
  <Button asChild>
    <Link to="/orgs/$org/status-updates/new" …>
      <Plus className="mr-2 h-4 w-4" />
      New update
    </Link>
  </Button>
</div>
```

So this is the page the user is flagging: "the Status Update page doesn't have
the correct design, like the Dashboard." The Dashboard uses `PageHeader`; Status
Updates doesn't, so its title looks different (bigger/bolder `text-3xl` font,
bare icon instead of a tiled icon).

## Goal

Bring the Status Updates **list** page header into line with the canonical
`PageHeader` pattern, finishing the `-03` migration that skipped it.

## Behaviour

- Render the header via
  `<PageHeader icon={Megaphone} title="Status updates" description="Publish narrative updates on your status pages." actions={…} />`
  imported from `@/components/shared/page-header`, matching Dashboard/Status
  Pages exactly (tiled icon, `text-2xl` title, muted subtitle).
- Move the **"New update"** primary action into the `actions` prop — keep the
  exact same `Button asChild` + `Link` to `/orgs/$org/status-updates/new`, the
  `Plus` icon, the label, and `data-testid="status-updates-new"`.
- The **filters row** below the header (search input + status-page select) stays
  exactly where it is — it is a secondary toolbar on its own row, not part of
  the header (consistent with `-03`'s scope rules).
- Preserve the existing copy verbatim (hardcoded English strings — these are not
  translation keys today; don't introduce i18n keys as part of this change).
- The `Megaphone` icon stays the same, just rendered inside the tile.
- Remove the now-unused inline wrapper markup. Drop the `Megaphone` import only
  if it's no longer referenced — note the empty-state at
  `status-updates.index.tsx:355` also uses `Megaphone`, so the import must stay.

## Out of scope

- The Status Updates **new** / **edit** pages
  (`status-updates.new.tsx:39-40`, `status-updates.$updateUid.edit.tsx:58-59`)
  use the inline `text-3xl` edit-page header with the back-arrow cluster. Those
  are detail/edit pages and were explicitly out of scope in `-03`; leave them
  unchanged here.
- No wording, icon, route, or filter-row changes.
- No new shared component — `PageHeader` already exists; just consume it.

## Testing

- `make dev-test`, open `/dash0/orgs/default/status-updates`, confirm the header
  now matches Dashboard/Status Pages (tiled icon, `text-2xl` title, muted
  subtitle, "New update" button right-aligned). Check desktop + mobile across
  the `sm` breakpoint, light + dark.
- The "New update" button must still navigate to `/status-updates/new`; the
  `data-testid="status-updates-new"` selector must keep working.
- Run the dash0 e2e suite (`make test-dash`) — if the `-03` style/e2e test
  enumerates pages that must use `PageHeader`, add Status Updates to it so the
  catalog and the route stay in agreement.
