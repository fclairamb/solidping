---
model: sonnet
effort: medium
---

# Check-group header menu duplicates the edit page; the edit page lacks delete and a breadcrumb

## Problem

On the checks list (`/orgs/$org/checks`), every check-group header carries two
row actions: a `Pencil` edit button that navigates to the group's edit page,
plus a `MoreVertical` dropdown menu (`group-menu-button` in
`web/dash0/src/routes/orgs/$org/checks.index.tsx`) containing Rename, Move
Up/Down, and Delete Group. This violates the repo's own row-action convention
("prefer ghost icon buttons over a `MoreVertical` menu",
`web/dash0/CLAUDE.md`), and the Rename item opens a modal dialog that
duplicates the name field of the dedicated edit route
(`/orgs/$org/check-groups/$uid/edit`) — editing is supposed to go through a
route, not a dialog.

Meanwhile the group edit page
(`web/dash0/src/routes/orgs/$org/check-groups.$uid.edit.tsx`) has two gaps:

1. **No delete action.** The only way to delete a group is the list-page menu
   being removed above.
2. **No breadcrumb.** The `Breadcrumbs` component in
   `web/dash0/src/routes/orgs/$org.tsx` matches sections by route-id prefix,
   and `/orgs/$org/check-groups/...` matches neither the
   `/orgs/$org/checks` prefix (string-wise `check-groups` ≠ `checks`) nor any
   other branch, so the header renders no breadcrumb at all on that page.

## Proposal

**Checks list (`checks.index.tsx`):**

- Remove the group-header `DropdownMenu` (Rename / Move Up / Move Down /
  Delete Group) entirely, along with the now-orphaned rename dialog,
  delete-group dialog, their state, and the `useDeleteCheckGroup` usage.
- Keep reordering as two direct ghost icon buttons in the header
  (`ArrowUp` / `ArrowDown`, test ids `group-move-up-button` /
  `group-move-down-button`), disabled on the first/last group respectively,
  next to the existing `Pencil` edit button.
- Rename is covered by the edit page's name field; delete moves to the edit
  page.

**Group edit page (`check-groups.$uid.edit.tsx`):**

- Add a `Delete Group` button (`variant="destructive"`, `Trash2` icon,
  `ml-auto` in the page header, test id `group-delete-button`) that opens an
  `AlertDialog` confirmation (`confirm-delete-group`) and, on confirm, calls
  `useDeleteCheckGroup` and navigates back to the checks list. Reuse the
  existing `dialog.deleteGroupTitle` / `dialog.deleteGroupDescription` /
  `toast.groupDeleted` translation keys.

**Breadcrumb (`$org.tsx`):**

- Add an `isCheckGroups` section branch (`routeId.startsWith("/orgs/$org/check-groups")`)
  rendering `Checks › {group name} › Edit`, with `Checks` linking back to the
  checks list. Resolve the group name with `useCheckGroup`, gated on the
  section flag since the `uid` param name is shared with on-call and
  escalation-policies routes.

**Cleanup & tests:**

- Drop the now-unused `menu.rename`, `dialog.renameGroupTitle`,
  `toast.groupRenamed`, `toast.groupRenameFailed` keys from all four locales.
- Update `web/dash0/e2e/check-groups.spec.ts`: rename and delete flows go
  through the edit page; reorder uses the direct header buttons.
  `check-group-escalation.spec.ts` already uses the edit page and needs no
  change.

## Implementation Plan

The working tree already contained uncommitted changes implementing this
exact spec (list-page menu removal, edit-page delete button + breadcrumb,
locale cleanup, e2e updates). Rather than rewrite from scratch, this pass:

1. Diffed each modified file against the Proposal above and verified it
   line-by-line:
   - `checks.index.tsx`: `DropdownMenu` (Rename/Move Up/Move Down/Delete
     Group), its dialogs, and `useDeleteCheckGroup`/rename state are gone;
     two direct ghost icon buttons (`ArrowUp`/`ArrowDown`,
     `group-move-up-button`/`group-move-down-button`, disabled on
     first/last) sit next to the existing `Pencil` edit button. Confirmed
     `DropdownMenu`, `MoreVertical`, `Trash2`, `Pencil` imports are still
     used elsewhere in the file (per-check row menu), so nothing is
     orphaned.
   - `check-groups.$uid.edit.tsx`: adds a `Delete Group` button
     (`variant="destructive"`, `Trash2`, `ml-auto`,
     `group-delete-button`) opening an `AlertDialog`
     (`confirm-delete-group`) that calls `useDeleteCheckGroup` and
     navigates back to `/orgs/$org/checks` on confirm, reusing
     `dialog.deleteGroupTitle` / `dialog.deleteGroupDescription` /
     `toast.groupDeleted`.
   - `$org.tsx`: adds `isCheckGroups` (`routeId.startsWith("/orgs/$org/check-groups")`)
     branch rendering `Checks › {group name} › Edit`, resolving the name
     via `useCheckGroup(org, isCheckGroups ? (params.uid ?? "") : "")` —
     gated the same way the on-call/escalation-policy sections gate their
     shared `uid` param, and confirmed `useCheckGroup` is a no-op query
     (`enabled: !!org && !!uid`) when passed `""`.
   - Locale files (`de`/`en`/`es`/`fr` `checks.json`): `menu.rename`,
     `dialog.renameGroupTitle`, `toast.groupRenamed`,
     `toast.groupRenameFailed` removed; grepped the whole `src/` and `e2e/`
     tree to confirm no remaining references to those keys or to the
     removed test ids (`group-menu-button`, `group-rename-action`,
     `group-delete-action`, `group-move-up-action`,
     `group-move-down-action`, `rename-group-input`,
     `rename-group-submit`).
   - `e2e/check-groups.spec.ts`: rename and delete flows now navigate to
     the edit page (`group-edit-button` → `group-edit-name-input` /
     `group-edit-submit`, or → `group-delete-button` →
     `confirm-delete-group`) and assert a return to `/checks`; reorder
     uses `group-move-up-button` directly. Verified all referenced test
     ids exist in the corresponding component files.
     `check-group-escalation.spec.ts` was checked and needs no change (no
     references to any removed menu/test id).
2. No functional gaps found — the WIP was complete and matched the
   Proposal. Remaining work: run `make fmt`, `make build-dash0`,
   `bun run lint` (dash0-scoped QA), and attempt to run the extended e2e
   spec if the local devloop is in test mode.
