# Status page detail: section drag-drop, section edit, delete-page button

## Context

On the status page detail view
(`web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx`)
three management gaps remain after the recent
[`2026-05-04-08-status-page-resource-reorder`](../done/2026/05/2026-05-04-08-status-page-resource-reorder.md)
work:

1. **Sections cannot be reordered.** Resources inside a section can be
   dragged (lines 385-490 + 589-613, using `@dnd-kit`,
   `useReorderResources` from `web/dash0/src/api/hooks.ts:1200`), but
   the *list of sections itself* renders in a plain `<div>` with no
   drag affordance and no up/down arrows
   (`status-pages.$statusPageUid.index.tsx:746-756`). Sections already
   carry a `position int` on both
   `server/internal/db/models/status_page.go` and the API layer
   (`server/internal/handlers/statuspages/service.go:91`), and
   `UpdateSectionRequest.Position` (line ~175) accepts position
   updates — but there is no UI and no bulk `reorder` endpoint
   equivalent to the resources one
   (`POST .../sections/{sectionUid}/resources/reorder`,
   `service.go:287`).
2. **Sections cannot be edited.** `useUpdateSection` exists
   (`hooks.ts:1124`) and the backend PATCH endpoint exists
   (`statuspages/handler.go:183`), but the detail page exposes only
   Add (`AddSectionDialog`, lines 153-239) and Delete (trash icon in
   the section header, lines 558-583). There is no way to rename a
   section or change its slug from the UI.
3. **The status page itself cannot be deleted from its detail page.**
   The header (lines 694-738) shows only "View public" and "Edit"
   buttons. To delete the page the operator has to navigate back to
   the list view and use the row dropdown
   (`status-pages.index.tsx:109-115`). For a destructive operation
   that's an awkward two-step.

These three gaps came up together as detail-page management asks; they
share the same file and the same pattern (mirror what already exists
for resources / for top-level pages), so they ship together.

## Goal

On the status page detail view:

- Sections can be reordered by drag-and-drop, with the same UX as
  resource reordering (grip handle, optimistic update, persisted via a
  single bulk `reorder` call).
- Each section header gains an Edit affordance for renaming the
  section and changing its slug.
- The header gains a Delete button for the status page, mirroring the
  existing list-page delete flow.

## Approach

### 1. Section drag-drop

**Backend** — add `POST /api/v1/orgs/{org}/status-pages/{statusPageUid}/sections/reorder`
mirroring the resource reorder endpoint
(`server/internal/handlers/statuspages/service.go:676-712`,
`handler.go:287`). Body: `{"uids": ["...", "..."]}` listing every
section UID in the new order. Validation rules — same as resources:
the UID list size must equal the page's section count, no duplicates,
no unknown UIDs; otherwise return `ErrReorderUIDsMismatch` (400). The
service writes positions sequentially in a single transaction. A new
`db.ReorderStatusPageSections(ctx, statusPageUID, []string)` mirrors
the resources counterpart.

**Frontend hook** — add `useReorderSections(org, statusPageUid)` next
to `useReorderResources` in `hooks.ts:1200`. Same shape:
`POST .../sections/reorder` with `{uids}`, optimistic update of the
`["status-page", org, statusPageUid, "sections"]` (and parent
`["status-page", org, statusPageUid, …with-sections]`) cache to
prevent the snap-back fixed in commit 02a2460e for resources.

**Frontend UI** — in `status-pages.$statusPageUid.index.tsx`, wrap the
section list (lines 746-756) in `DndContext` + `SortableContext` (mirror
the resource setup at lines 589-613). Add a `useSortable` hook to
`SectionCard` and render a `GripVertical` drag handle as the leftmost
element of the section header (lines 542-585), styled identically to
the resource handle (`touch-none cursor-grab`, ghost color). Use the
same `PointerSensor` (4px activation) + `KeyboardSensor` setup. On
`onDragEnd`, compute the new order with `arrayMove` and call
`reorderSections.mutate(uids)`.

Drop the up/down chevrons that exist on resources (lines 411-430) for
sections — they'll be redundant with the handle and clutter the header
that now has Edit + Delete + Add Resource buttons. (Keep them on
resources for now; that's a separate cleanup.)

### 2. Section edit

Generalize `AddSectionDialog` (lines 153-239) into a single
`SectionDialog` that takes optional `section?: StatusPageSection` to
toggle create vs edit mode:

- No `section` → POST via `useCreateSection`, title "Add section",
  fields blank.
- `section` provided → PATCH via `useUpdateSection` with
  `{name, slug}`, title "Edit section", fields prefilled.

In `SectionCard` header (lines 550-584), add a Pencil icon button to
the right of `AddResourceDialog` and to the left of the Delete trash,
that opens `<SectionDialog section={section}>`. Use ghost variant,
size icon, matching the existing buttons.

Modal vs route: dash0's "editing always changes the route" convention
(`web/dash0/CLAUDE.md`) makes an explicit exception for trivial
single-field renames, and explicitly applies to "anything with a
multi-field form". Sections have two fields (name + slug). The
existing Add is a modal; for symmetry on the same surface I propose a
modal here too, and treat the Add-and-Edit-as-routes migration as a
separate, larger refactor if the team decides to fully enforce the
convention everywhere. Call this out in the PR description.

### 3. Delete-status-page button on the detail header

In the header action group (lines 694-738), add a Delete button after
Edit:

- Variant `outline`, size `sm`, with `Trash2` icon and red text
  (`text-destructive`) so it reads destructive without being shouty.
- Tooltip / `aria-label` "Delete status page".
- Wraps an `AlertDialog` whose copy mirrors the list-page dialog
  (`status-pages.index.tsx:246-264`).
- On confirm, call `useDeleteStatusPage(org).mutateAsync(statusPageUid)`
  (`hooks.ts:1080`), toast `t("statusPages:toast.deleted")`, and
  navigate to `/orgs/$org/status-pages`.

Do not remove the list-page delete affordance — it's the natural
entry point for bulk-style ops and matches the row-actions spec
([`2026-05-08-07`](2026-05-08-07-status-pages-list-row-actions.md)).

## Files to change

**Backend**
- `server/internal/handlers/statuspages/service.go` — `ReorderSections`
  + `ErrReorderSectionsUIDsMismatch` (or reuse the resources error)
- `server/internal/handlers/statuspages/handler.go` — route + handler
- `server/internal/app/server.go` — register the route
- `server/internal/db/postgres/...` and `server/internal/db/sqlite/...`
  — `ReorderStatusPageSections` impl
- `server/internal/handlers/statuspages/service_test.go` — table test
  for the new reorder method (success, mismatch, unknown UID,
  duplicates)

**Frontend**
- `web/dash0/src/api/hooks.ts` — `useReorderSections`
- `web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx`
  — section dnd wiring, generalized `SectionDialog`, page-delete
  button + dialog
- `web/dash0/src/locales/{en,de,es,fr}/statusPages.json` —
  `sections.edit`, `sections.editTitle`, `detail.delete`,
  `detail.deleteDescription`, etc.

## Verification

1. `make test` (backend reorder service tests) and `make test-dash`
   pass.
2. `make dev`, open a status page with ≥3 sections each containing
   resources.
3. Drag a section from position 0 to position 2 by its grip handle;
   network shows a single `POST .../sections/reorder` and the order
   persists across reload. Public status page reflects the new order.
4. Click the Pencil icon on a section header, change name and slug,
   save → toast "Section updated", header re-renders with new values,
   slug also reflected on the public page.
5. Click Delete on the detail-page header, confirm → redirected to
   `/orgs/$org/status-pages`, the deleted page is gone from the list.
6. Add a Playwright spec at `web/dash0/e2e/status-page-section-reorder.spec.ts`
   mirroring `status-page-reorder.spec.ts` (resource version) for the
   section drag.
