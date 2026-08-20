---
model: sonnet
effort: high
---

# SLO form & detail UX: picker shows a UUID, no slug auto-generation, empty breadcrumb, inline edit instead of a dedicated route

## Problem

Four defects around `/dash0/orgs/$org/slos/*`:

**1. Picker shows a UUID instead of the check's name.** On `/slos/new`,
picking a check under **Scope → Check** makes the picker trigger display the
check's raw UUID (e.g. `e6e1d710-610c-4f93-9214-f7bfd6438e04`) instead of its
name. Same for the **Check group** scope, and on the detail page's edit form
the initial value renders as a UUID too.

Root cause: `CheckPicker` falls back to the raw value when no `selectedLabel`
prop is provided —
[check-picker.tsx:62](web/dash0/src/components/shared/check-picker.tsx:62):

```tsx
const triggerLabel =
  selectedLabel ?? (value ? value : placeholder ?? t("dependencies:pickCheck"));
```

`CheckGroupPicker` has the identical fallback
([check-group-picker.tsx:70](web/dash0/src/components/shared/check-group-picker.tsx:70)).
The SLO form renders both pickers without `selectedLabel`
([slo-form.tsx:124-140](web/dash0/src/components/slos/slo-form.tsx:124)), so
the UUID leaks into the UI. Other callers each hand-roll the workaround:
badges ([badges.tsx:451](web/dash0/src/routes/orgs/$org/badges.tsx:451)) and
status pages
([status-pages.$statusPageUid.index.tsx:427](web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx:427))
keep a `selectedLabel` `useState` beside the uid and thread it into the picker.

**2. Slug is not auto-generated from the name.** Every other name+slug form
derives the slug from the name as the user types (until the slug is manually
edited) — the canonical pattern is on the design reference page
([design-reference.tsx:1737](web/dash0/src/routes/orgs/$org/design-reference.tsx:1737),
`NameSlugExample`: `slugify()` from `@/lib/utils` + a `slugManuallyEdited`
flag), implemented for real in e.g.
[status-page-form.tsx:97-101](web/dash0/src/components/shared/status-page-form.tsx:97).
The SLO form keeps two independent `useState`s
([slo-form.tsx:40-41](web/dash0/src/components/slos/slo-form.tsx:40)) and never
calls `slugify`, so the slug stays empty unless typed by hand.

**3. The breadcrumb is empty on every SLO route.** After creating an SLO (and
on `/slos`, `/slos/new`, `/slos/$uid`) the top bar shows only the sidebar
toggle and a separator — no breadcrumb at all. The `Breadcrumbs` component is
a hand-maintained list of per-section branches
([$org.tsx:122-156](web/dash0/src/routes/orgs/$org.tsx:122)) and no `isSlos`
branch was added when the SLO section shipped, so no branch matches and
nothing renders.

**4. Editing is an inline form at the bottom of the detail page.** The detail
route renders `SloForm mode="edit"` inline below the attainment/history cards
([slos.$uid.tsx:192-207](web/dash0/src/routes/orgs/$org/slos.$uid.tsx:192)).
The repo convention (top-level `CLAUDE.md`, `web/dash0/CLAUDE.md` "Editing
always changes the route") is a dedicated edit route rendering the same form
component as `/new` — exactly how maintenance windows do it
(`maintenance-windows/$maintenanceWindowUid/edit`,
[$org.tsx:152-154](web/dash0/src/routes/orgs/$org.tsx:152)).

## Proposal

### 1. Picker label — fix the component, not the caller

Make `CheckPicker` and `CheckGroupPicker` resolve their own trigger label so
no caller can regress into showing a UUID:

- Keep the last-selected entity's name in local state when `select()` fires
  (the full object is already in hand). For an initial `value` set before any
  selection (edit pages, restored state), resolve the name from the
  already-fetched list (`useChecks` / the groups query) when the uid is
  present in it, otherwise fetch the single entity by uid
  (`GET /api/v1/orgs/$org/checks/$uid`, and the group equivalent) via a small
  query hook. While resolving, show the placeholder or a muted `…` — never the
  raw uid. Fall back to the uid only if the entity genuinely no longer exists
  (deleted check), so the field stays clearable.
- Keep `selectedLabel` as an override for callers that already pass it
  (badges, status pages) — explicit prop wins over self-resolution, so this
  change is backward-compatible. Optionally simplify those callers to drop
  their hand-rolled state in a follow-up; not required here.

### 2. Slug auto-generation — follow the design-reference pattern

In `slo-form.tsx`, adopt the canonical name+slug pairing for **create** mode:
`setSlug(slugify(name))` on name change while a `slugManuallyEdited` flag is
false; typing in the slug field sets the flag (clearing the slug may re-enable
auto-generation, matching `NameSlugExample`). Edit mode keeps the stored slug
untouched, mirroring `status-page-form.tsx`'s `mode === "create"` guard.

### 3. Breadcrumb — add the SLOs section branch

In `Breadcrumbs` ([$org.tsx](web/dash0/src/routes/orgs/$org.tsx)), add an
`isSlos` section modeled on the maintenance-windows branch: a root `SLOs`
crumb linking to `/orgs/$org/slos`, a `New` leaf on `/slos/new`, and on
`/slos/$uid` (and the new edit route) a leaf label from `useSlo(org, uid)`
(the hook the detail page already uses,
[slos.$uid.tsx:69](web/dash0/src/routes/orgs/$org/slos.$uid.tsx:69)) — gated
on the section flag since the `$uid` param name is shared with on-call and
escalation-policies. Add the nav label to the `nav` locale files alongside the
existing section names.

### 4. Edit on a dedicated route

- Add `/orgs/$org/slos/$uid/edit` rendering the same `SloForm mode="edit"`
  page-style as `/slos/new` (restructure `slos.$uid.tsx` into
  `slos.$uid.index.tsx` + `slos.$uid.edit.tsx` if needed by the file-router).
- Remove the inline form from the detail page and add an **Edit** action
  (Pencil, per row-action conventions) linking to the edit route.
- On save, navigate back to the detail page; on cancel, likewise.

### Tests

- Playwright E2E (`web/dash0/e2e/`): on `/slos/new`, select a check and assert
  the trigger shows the check's **name** and not a UUID pattern
  (`/[0-9a-f]{8}-/` must not match). Repeat for the group scope. On the SLO
  edit route, assert the pre-selected scope renders the name after load.
- E2E: on `/slos/new`, type a name and assert the slug field fills with the
  slugified name; edit the slug manually, change the name again, and assert
  the slug no longer follows.
- E2E: breadcrumb assertions on `/slos`, `/slos/new`, and the detail page
  (root crumb + leaf shows the SLO's name after creation).
- E2E: detail page has no inline edit form; the Edit action navigates to
  `/slos/$uid/edit`, saving returns to the detail page with the change
  visible.
- Cover the deleted-entity fallback if cheap (picker with a uid that 404s
  renders the uid rather than crashing).

## Implementation Plan

1. **Picker label self-resolution** (`check-picker.tsx`, `check-group-picker.tsx`):
   keep a `pickedUid`/`pickedLabel` pair in local state, set synchronously by
   `select()`. For a `value` that didn't come from this component's own
   `select()` (initial/edit/restored), resolve the label first from the
   already-fetched list (`matches` from `useChecks`, `groups` from
   `useCheckGroups`), and only if the uid isn't in that list, fall back to a
   single-entity fetch (`useCheck` / `useCheckGroup`, both already exist —
   no new hook needed). Show `"…"` (muted) while resolving; fall back to the
   raw uid only once the single-entity fetch errors (deleted entity).
   `selectedLabel` stays an explicit override that short-circuits all of the
   above, so badges.tsx and status-pages.$statusPageUid.index.tsx keep
   working unchanged.
2. **Slug auto-generation** (`slo-form.tsx`): add a `slugManuallyEdited` flag
   initialized to `mode === "edit"`. The name field's `onChange` also calls
   `setSlug(slugify(value))` when `mode === "create" && !slugManuallyEdited`;
   the slug field's `onChange` sets the flag. Matches the inline-onChange
   shape of the design reference's `NameSlugExample`, gated by
   `status-page-form.tsx`'s create-only guard.
3. **Breadcrumbs** (`$org.tsx`): add `isSlos`/`isSlosNew`/`isSlosDetail`/
   `isSlosEdit` route-id flags, a `useSlo(org, isSlosDetail || isSlosEdit ?
   params.uid : "")` fetch (gated because `uid` is shared with on-call and
   escalation-policies), and a render branch modeled 1:1 on the existing
   `isMaintenance` branch (root crumb / New leaf / detail-or-edit leaf / Edit
   leaf). The `nav:slos` label already existed in all four locale files
   (used by the sidebar) — nothing to add there.
4. **Dedicated edit route**: split `slos.$uid.tsx` into a thin
   `<Outlet />` layout, `slos.$uid.index.tsx` (the detail page, inline
   `SloForm` removed, `PageHeader` + Edit action added — Pencil icon button
   linking to the edit route) and `slos.$uid.edit.tsx` (loads the SLO,
   renders `SloForm mode="edit"`, save/cancel both navigate back to
   `/orgs/$org/slos/$uid`). The list page's row Edit icon now links to
   `/slos/$uid/edit` instead of the detail route.
5. QA: `make build-dash0` (tsc + vite build, regenerates `routeTree.gen.ts`)
   and `bun run lint` scoped to touched files (repo-wide eslint is red on
   base from pre-existing errors; gate is no *new* ones). Author Playwright
   E2E for all five spec scenarios in `web/dash0/e2e/slos.spec.ts`
   (extending the existing SLO E2E file if present), run them against a
   side-car test server per `web/dash0/CLAUDE.md` / repo `CLAUDE.md`
   conventions.
