# Fix on-call edit route (`/on-call/$slug/edit` renders nothing)

## Symptom

`http://localhost:4000/dash0/orgs/default/on-call/platform-eu/edit` shows the
detail page, not the edit form. The URL bar updates, the route is registered
in `routeTree.gen.ts`, but the `EditOnCallSchedulePage` component never paints.

## Root cause

The on-call routes don't follow the codebase's layout-and-index convention.

```
web/dash0/src/routes/orgs/$org/
  on-call.tsx                  # /on-call layout (Outlet) ✓
  on-call.index.tsx            # /on-call list ✓
  on-call.new.tsx              # /on-call/new ✓
  on-call.$slug.tsx            # /on-call/$slug — renders the detail directly, NO <Outlet/>
  on-call.$slug.edit.tsx       # /on-call/$slug/edit — child of $slug, never rendered
```

In TanStack Router file-routing, `on-call.$slug.edit.tsx` is a child of
`on-call.$slug`. When the URL is `/on-call/$slug/edit`, the router matches
both routes and renders the parent's component, which is expected to render
`<Outlet />` for the child. `on-call.$slug.tsx` returns the full
`OnCallDetailPage` JSX with no Outlet, so the edit page is silently dropped
and the user just sees the detail page again.

The other slug-based detail routes already have the right shape:

| Route | Parent file | Index file | Edit file |
|---|---|---|---|
| Checks | `checks.$checkUid.tsx` (Outlet) | `checks.$checkUid.index.tsx` | `checks.$checkUid.edit.tsx` |
| Status pages | `status-pages.$statusPageUid.tsx` (Outlet) | `status-pages.$statusPageUid.index.tsx` | `status-pages.$statusPageUid.edit.tsx` |
| **On-call** | **`on-call.$slug.tsx` (full detail page — bug)** | **missing** | `on-call.$slug.edit.tsx` |

## Goal

Bring on-call into line with the canonical pattern so that
`/on-call/$slug` keeps rendering the detail page and `/on-call/$slug/edit`
renders the edit form.

## Scope

### In scope

1. Convert `on-call.$slug.tsx` to a layout-only component (just an
   `<Outlet />`), matching `checks.$checkUid.tsx` and
   `status-pages.$statusPageUid.tsx` byte-for-byte.
2. Create `on-call.$slug.index.tsx` that holds the current
   `OnCallDetailPage` body — i.e. cut the entire component (state, hooks,
   `CalendarStrip`, JSX) out of `on-call.$slug.tsx` and paste it into
   `on-call.$slug.index.tsx`. Update its `createFileRoute` to
   `/orgs/$org/on-call/$slug/`.
3. Regenerate the TanStack route tree (`bun run dev` does this on file
   save; CI runs `tsr generate`). Verify `routeTree.gen.ts` now has
   `OrgsOrgOnCallSlugIndexRoute` next to the existing
   `OrgsOrgOnCallSlugRoute` and `OrgsOrgOnCallSlugEditRoute`.
4. Smoke-test by hand:
   - `/on-call` → list renders
   - `/on-call/$slug` → detail renders (back link, schedule cards)
   - `/on-call/$slug/edit` → edit form renders, prefilled with the
     schedule's current values, save returns to the detail page
5. Update or add a Playwright test under `web/dash0/e2e/` that visits
   the edit URL directly and asserts the form is present (regression
   guard: hitting the URL must render the edit form, not the detail).

### Out of scope

- Visual changes to the edit form, detail page, or list page. A separate
  spec (`2026-05-09-05-oncall-pages-button-and-header-polish.md`) covers
  the design-reference alignment for buttons and headers across all four
  on-call routes — keep this spec mechanical so its diff stays reviewable.
- Changes to `on-call.$slug.edit.tsx` itself (it's already correct).
- Backend changes — none needed.

## Implementation plan

1. **Read** `on-call.$slug.tsx`, `checks.$checkUid.tsx`, and
   `checks.$checkUid.index.tsx` side-by-side to confirm the target shape.
2. **Create** `web/dash0/src/routes/orgs/$org/on-call.$slug.index.tsx`:
   - `createFileRoute("/orgs/$org/on-call/$slug/")`
   - Component: the `OnCallDetailPage` and `CalendarStrip` definitions
     verbatim, with all imports moved over.
3. **Replace** `web/dash0/src/routes/orgs/$org/on-call.$slug.tsx` with the
   3-line Outlet layout:
   ```tsx
   import { createFileRoute, Outlet } from "@tanstack/react-router";

   export const Route = createFileRoute("/orgs/$org/on-call/$slug")({
     component: () => <Outlet />,
   });
   ```
   (Match the inline-arrow style used by `status-pages.$statusPageUid.tsx`
   if simpler, otherwise the named function variant — both are accepted.)
4. **Run** `cd web/dash0 && bun run dev` once to let TSR regenerate
   `routeTree.gen.ts`. Commit the regenerated file alongside the source
   edits.
5. **Run** `bun run lint` and `bun run build` (`make build-dash0`) to
   confirm no broken imports.
6. **e2e** — extend `web/dash0/e2e/on-call.spec.ts` (create if missing)
   with a test that:
   - Creates a schedule via the API or UI
   - Navigates directly to `/dash0/orgs/$org/on-call/$slug/edit`
   - Asserts the schedule's name input is visible and prefilled
   - Edits the name, saves, and asserts the new name on the detail page

## Acceptance criteria

- [ ] `on-call.$slug.tsx` is a layout-only file (≤10 lines, just renders
  `<Outlet />`).
- [ ] `on-call.$slug.index.tsx` exists and holds the detail-page logic.
- [ ] Visiting `/dash0/orgs/default/on-call/<existing-slug>/edit` renders
  the edit form with values prefilled.
- [ ] Visiting `/dash0/orgs/default/on-call/<existing-slug>` still renders
  the detail page (no regression on the index route).
- [ ] Browser back from `/edit` returns to the detail page.
- [ ] `bun run lint` and `make build-dash0` pass.
- [ ] An e2e test pins the edit-URL behaviour so this regression cannot
  recur silently.

## Files affected

- `web/dash0/src/routes/orgs/$org/on-call.$slug.tsx` — replace body with Outlet
- `web/dash0/src/routes/orgs/$org/on-call.$slug.index.tsx` — new, holds detail page
- `web/dash0/src/routeTree.gen.ts` — regenerated
- `web/dash0/e2e/on-call.spec.ts` — new test (or extension)

## Notes

- Tests in `web/dash0/e2e/` already use the `data-testid="oncall-edit-button"`
  selector. Keep the testid intact when the JSX moves to
  `on-call.$slug.index.tsx` so existing tests remain green.
- The original move was scoped in
  `specs/done/2026/05/2026-05-07-04-edit-on-route-convention-and-oncall-edit-page.md`
  but the layout split was missed. Reference that spec in the PR description
  and link this one as the follow-up so the trail is clear.
