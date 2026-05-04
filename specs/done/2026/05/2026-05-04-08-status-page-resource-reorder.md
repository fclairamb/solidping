# Status page: restore reordering of checks within a section

## Context

The data model and API both already support per-resource ordering:
- `server/internal/db/models/status_page.go:96-105` — `StatusPageResource{Position int}`.
- `server/internal/db/models/status_page.go:122-126` — `StatusPageResourceUpdate{Position *int}`.
- `server/internal/handlers/statuspages/service.go:185` — `UpdateResourceRequest.Position *int`.
- `PATCH /api/v1/orgs/:org/status-pages/:uid/sections/:sectionUid/resources/:resourceUid` accepts the position update.

The dash0 status page editor (`web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx`) renders a `<GripVertical>` icon (line ~346) suggesting drag-and-drop, but **there are no drag handlers wired** — the icon is purely decorative. Users cannot reorder resources within a section.

The user's "restore" wording suggests this used to work. Git log hasn't surfaced a prior implementation that was removed; treat it as a partial implementation that was never finished. Ship the missing half.

## Scope

**In scope:**
- Drag-and-drop reordering of resources within a single section.
- Frontend persists the new order via `PATCH …/resources/:resourceUid` with `{position: <newIndex>}`.
- Optimistic UI update; reconcile on response.
- Playwright E2E covering the drag.

**Out of scope:**
- Reordering sections themselves (separate concern; can follow up if the chosen lib trivially supports it).
- Moving a resource across sections — different operation.
- Touchscreen drag UX polish beyond what `@dnd-kit` provides out of the box.

## Approach

### 1. Choose a library

Use `@dnd-kit/core` + `@dnd-kit/sortable` (small, accessible, modern, works with React 18+). Check `web/dash0/package.json` first; if neither is present, add both.

### 2. Wire the section list

`web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx`:

Wrap each section's resource list in a `<DndContext>` + `<SortableContext>`. Convert `ResourceRow` to a sortable item using `useSortable({id: resource.uid})`. Bind the drag-handle ref/listeners to the existing `<GripVertical>` icon (line ~346) — `setActivatorNodeRef` / `listeners` from the hook.

On `onDragEnd`:
1. Compute the new array order locally with `arrayMove`.
2. Update local state optimistically.
3. Compute the `position` for the dropped item: simplest is the new array index (0-based). If the backend uses sparse positions, compute a midpoint between neighbors.
4. Fire a `PATCH` to `/orgs/:org/status-pages/:uid/sections/:sectionUid/resources/:resourceUid` with `{position: newIndex}`.
5. On error, revert and show a toast.

If the backend re-densifies positions on every update (recommended), call only one PATCH per drag (the moved resource). If it doesn't, do one PATCH per resource whose index changed — chatty but correct. Read the service code to decide.

### 3. Backend re-densification (verify, possibly add)

Check `server/internal/handlers/statuspages/service.go` `UpdateResource` — does it renumber siblings to keep `position` contiguous? If not, the frontend either:
- sends multiple PATCHes (acceptable for typical N≤20 resources), or
- relies on a server-side renumbering helper (cleaner; one PATCH per drag).

If renumbering is missing, add it: `UpdateResource` with a non-nil `Position` should renumber siblings within the same section so `position ∈ [0, N-1]`. Wrap in a transaction.

### 4. Tests

**Backend** (`server/internal/handlers/statuspages/service_test.go`):
- Move a resource from index 2 to index 0; assert all three resources have positions `[0,1,2]` after the operation.

**Playwright E2E** (`web/dash0/e2e/status-page-reorder.spec.ts`):
1. Open a status page with a section containing ≥3 resources.
2. Drag resource A from position 0 to position 2 (use Playwright's `dragTo`).
3. Reload the page.
4. Assert the rendered order matches the new arrangement.

## Verification

1. `make test` and `make test-dash` pass.
2. `make dev`, edit a status page with ≥3 resources, drag one — order updates locally, persists across reload.
3. Network panel shows a single PATCH per drop (assuming backend renumbering).
4. Public status page (status0) reflects the new order on next fetch.
