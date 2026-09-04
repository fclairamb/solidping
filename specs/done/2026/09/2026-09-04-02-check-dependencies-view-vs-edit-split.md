---
model: opus
effort: high
---

# Check dependencies are edited on the wrong page, and the edit page can only add/remove them

## Problem

Dependencies are exposed in two places, and each one does the wrong half of the job.

**The check detail page is an editor.** [`dependencies-card.tsx`](../../web/dash0/src/components/checks/dependencies-card.tsx)
renders every "Depends on" row with a pencil and a trash bin, an inline kind/description
editor ([`DependsOnRow`](../../web/dash0/src/components/checks/dependencies-card.tsx:150)),
and a dashed "Pick a check… / Hard / Optional — what is the relationship? / Add dependency"
row ([`AddDependencyRow`](../../web/dash0/src/components/checks/dependencies-card.tsx:280))
that writes straight to the API. The detail page is the *view* surface for a check — every
other attribute is read-only there and edited on `/checks/$checkUid/edit`. Dependencies are
the one exception, so a user who opens the detail page to *look* at a check is handed a
live mutation form, and a user who opens the edit page to *change* the check finds a
weaker editor than the one on the view page.

**The edit page is a crippled editor.** The form section
([`form/sections/dependencies.tsx`](../../web/dash0/src/components/checks/form/sections/dependencies.tsx))
only lets you pick parents and remove them. Kind and description are not editable — the
helper text literally says "Edit kind/description on the check detail page after save."
Every parent added from the form is hard-coded to `kind: "hard"` with no description
([`checks.$checkUid.edit.tsx:153-158`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.edit.tsx:153),
[`checks.new.tsx:204-217`](../../web/dash0/src/routes/orgs/$org/checks.new.tsx:204)).
The section's rows don't even show the existing kind/description of a parent, so the user
can't tell a hard edge from a soft one while editing. The section is also the only
form section that isn't translated (hard-coded "Dependencies", "Add a parent check…",
"Remove parent") while the card is fully keyed under `dependencies:*`.

**Both lists look flat.** Each row is `rounded-md border p-2` on the same background as
the card/section it sits in (see the two screenshots attached to this spec's request: four
stacked outlined rectangles on the page background, then a dashed one for the add row). With
no surface contrast, no hover state and no separation between the row's identity, its kind
and its description, the list reads as a pile of identical boxes rather than a list of
distinct relationships. This is the same-background-list-items smell the design reference
already warns against for other surfaces.

## Proposal

Move all mutation into the edit form, make the detail card read-only, and give both lists
a proper row design. Backend and API are untouched — this is a dash0-only change.

### 1. Detail page: read-only card

In [`dependencies-card.tsx`](../../web/dash0/src/components/checks/dependencies-card.tsx):

- Delete `DependsOnRow`'s editing branch, the pencil button, the trash/`AlertDialog`
  branch and `AddDependencyRow` entirely. Drop the now-unused `useCreateCheckDependency`,
  `useUpdateCheckDependency`, `useDeleteCheckDependency`, `CheckPicker`, `Select`, `Input`,
  `AlertDialog*`, `formatCyclePath`, `toast`/`ApiError` imports.
- Both "Depends on" and "Depended on by" render the same read-only row component (check
  link + kind badge + description). Keep `DependencyWarnings` and the empty-state copy.
- Add a single "Edit" affordance in the card header (`Pencil` ghost button, or a small
  outline button) that links to `/orgs/$org/checks/$checkUid/edit` and lands the form with
  the Dependencies section open. The form already supports deep-linking a section via the
  `sectionOpen(...)` helper in
  [`check-form.tsx:1520`](../../web/dash0/src/components/shared/check-form.tsx:1520) — reuse
  whatever search/hash mechanism it keys off (add one if it's only state-driven).
- Retune the empty state: "No dependencies configured. Add a parent above…" no longer makes
  sense once the add row is gone. Point it at the edit page instead.

### 2. Edit page: full editor

Replace [`DependsOnFormSection`](../../web/dash0/src/components/checks/form/sections/dependencies.tsx)
with a section that carries everything the card used to:

- **Form state becomes `{ uid: string; label: string; kind: DependencyKind; description: string }[]`**
  instead of `{ uid, label }[]`. Seed it from `existingDeps.dependsOn` including each edge's
  `kind` and `description` (currently dropped at
  [`check-form.tsx:474-480`](../../web/dash0/src/components/shared/check-form.tsx:474)).
- **Each row shows** the parent's label, a kind `Select` (Hard/Soft, with the existing
  `kindHardTooltip`/`kindSoftTooltip` copy surfaced as help), a description `Input`, and a
  destructive `Trash2` remove button. Editing is inline in the row — this *is* the edit page,
  so no per-row pencil/save/cancel dance; changes are staged until the form's Save.
- **Add row**: `CheckPicker` + kind `Select` + description `Input` + "Add dependency", i.e.
  the current card's `AddDependencyRow` moved into the form. Exclude `checkUid`, already-picked
  parents, and — as the card does today via `ancestorsAndDescendants(graph, checkUid)` —
  descendants, so a cycle can't be staged in the first place. Keep the
  `DEPENDENCY_CYCLE`/`DEPENDENCY_DUPLICATE`/`CHECK_NOT_FOUND` error mapping (with
  `formatCyclePath`) for the save step, since the graph the picker was excluded against may
  be stale by the time the form is submitted.
- **Submit payload**: `CheckFormData.dependsOnParentUids` /
  `initialDependsOnParentUids` (`check-form.tsx:305-306`) become a list of
  `{ parentCheckUid, kind, description }` (name it `dependsOn`). Keep `initialDependsOn` so
  the edit page can diff.
- **Edit page sync** ([`checks.$checkUid.edit.tsx:141-162`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.edit.tsx:141))
  gains a third bucket: `toUpdate` = parents present in both sets whose kind or description
  changed → `useUpdateCheckDependency(...).mutateAsync({ uid: edge.uid, kind, description })`.
  `toAdd` passes the staged kind/description instead of `kind: "hard"`.
- **Create page** ([`checks.new.tsx:204-217`](../../web/dash0/src/routes/orgs/$org/checks.new.tsx:204))
  posts the staged kind/description instead of `kind: "hard"`.
- **Translate it**: the section uses `useTranslation(["dependencies", "common"])` like the
  card, reusing the existing `dependencies:*` keys (`dependsOn`, `dependsOnHelp`,
  `pickCheck`, `kindHard`, `kindSoft`, `descriptionPlaceholder`, `addDependency`,
  `remove`, `errors.*`). Any new key must be added to **all** locales under
  `web/dash0/src/locales/*/dependencies.json` — the unit suite fails on a missing key, and
  that suite is not part of the default `/implement-todos` gate, so run `bun run test:unit`
  explicitly.
- Delete the "Edit kind/description on the check detail page after save." sentence.
- The collapsible's `summary` (`depsSummary`) should now mention kinds, e.g. "2 hard, 1 soft".

### 3. Row design (both pages)

Add a **dependency row** pattern and register it in
[`design-reference.tsx`](../../web/dash0/src/routes/orgs/$org/design-reference.tsx) (near the
`list-surface` section at line ~4371) — it's the canonical fix for same-background stacked
items and other features will want it:

- Rows sit inside one bordered container and are separated with `divide-y`, not each
  wrapped in its own `rounded-md border`. Give the container `bg-card` (or `bg-muted/30` on
  the view page) so it contrasts with the page, and give rows `hover:bg-muted/50` on the
  view page where they're links.
- Fixed column structure instead of a free-flowing `flex-wrap`: identity (check link, bold)
  · kind badge · description (muted, truncates with title tooltip) · trailing actions.
  On mobile the description wraps under the identity — keep touch targets ≥ 40px per the
  frontend rules.
- Kind badge keeps the red/blue tint but as a small pill with a leading dot, matching the
  `customized` dot-pill style already used on the collapsible header, so hard vs soft is
  readable at a glance.
- The add row on the edit page stays visually distinct (dashed top border inside the same
  container, or a footer row) rather than a second floating dashed box.
- Empty state for "Depended on by" / "Depends on" renders inside the container as a muted
  row, not as a bare paragraph under a header.

### 4. Tests

- Update [`e2e/check-dependencies.spec.ts`](../../web/dash0/e2e/check-dependencies.spec.ts):
  it currently adds a dependency from the *detail* page (`Pick a check…` → `Add dependency`,
  lines 48-55). Rewrite that flow through the edit page and assert the detail page shows the
  row read-only (no pencil/trash present, link + badge visible). Keep the existing
  "deleting a check clears it from the other check's card" assertion.
- Add an e2e case: on the edit page, change an existing dependency's kind hard→soft and set
  a description, Save, and assert the detail page reflects both (badge text + description),
  proving the new `toUpdate` sync path.
- Add an e2e case: on the edit page, add a dependency with kind soft + description, Save,
  and assert the created edge is soft (proves the create path no longer hard-codes `hard`).
- Unit test the diff helper (add/remove/update buckets) if it's extracted from the edit
  route — extract it; the current inline version is untested.
- `bun run test:unit` must stay green (locale-key completeness).

### Out of scope

- Backend / API changes. `POST`/`PATCH`/`DELETE …/dependencies` already carry `kind` and
  `description`.
- The org-wide dependency graph page (`dependencies.index.tsx`) — it's read-only already;
  it may adopt the new row pattern if trivially reusable, but it's not required.

## Implementation Plan

1. **Shared row primitives** — new `web/dash0/src/components/checks/dependency-row.tsx`:
   `DependencyRowList` (one bordered `overflow-hidden rounded-lg border divide-y`
   container, `bg-muted/30` on the view page / `bg-card` in the form),
   `DependencyRow` (grid: identity · kind · description · actions; single column on
   mobile so the description wraps under the identity; `min-h-10` touch target;
   optional `hover:bg-muted/50` where rows are links), `DependencyKindBadge`
   (dot-pill matching the `customized` badge on `CollapsibleSection`), and
   `DependencyEmptyRow`.
2. **Detail card read-only** — `dependencies-card.tsx`: delete `DependsOnRow`'s
   editing branch, the pencil, the trash/`AlertDialog` and `AddDependencyRow`;
   drop the create/update/delete hooks, `CheckPicker`, `Select`, `Input`,
   `AlertDialog*`, `formatCyclePath`, `toast`/`ApiError` imports. Both lists render
   through the shared row. Card header gains a single `Edit` ghost link to
   `/orgs/$org/checks/$checkUid/edit?section=dependencies` (the edit route already
   validates `?section=` and `check-form`'s `sectionOpen` keys off it). Empty-state
   copy retuned to point at the edit page.
3. **Form section becomes the editor** — `form/sections/dependencies.tsx`
   rewritten: rows carry a kind `Select` + description `Input` + destructive
   `Trash2`, staged until Save; an add row (CheckPicker + Select + Input +
   "Add dependency") sits as a dashed footer row in the same container; the picker
   excludes self, already-picked parents and `ancestorsAndDescendants(graph, …)`
   descendants; `DEPENDENCY_CYCLE`/`DEPENDENCY_DUPLICATE`/`CHECK_NOT_FOUND` mapping
   with `formatCyclePath` is kept for the save step. Fully translated via
   `useTranslation(["dependencies","common"])`.
4. **Payload** — `CheckFormData.dependsOnParentUids`/`initialDependsOnParentUids`
   become `dependsOn`/`initialDependsOn`: `{ parentCheckUid, kind, description }[]`.
   `check-form.tsx` seeds form state from `existingDeps.dependsOn` **including**
   kind + description (the current drop at :474-480 is the bug). `depsSummary`
   reports kinds ("2 hard, 1 soft").
5. **Diff helper** — new `src/lib/dependency-diff.ts` exporting `diffDependencies`
   returning `{ toAdd, toUpdate, toRemove }`; unit-tested in
   `src/lib/dependency-diff.test.ts`. `checks.$checkUid.edit.tsx` calls it and runs
   the three buckets (create / patch / delete) with the staged kind + description;
   `checks.new.tsx` posts the staged kind + description instead of `kind: "hard"`.
6. **Design reference** — a "Dependency row" section registered in the SubNav and
   rendered next to `list-surface`.
7. **Locales** — new keys in all four `src/locales/*/dependencies.json`, guarded by
   a new `src/lib/dependencies-locales.test.ts` parity test.
8. **Tests** — `e2e/check-dependencies.spec.ts` rewritten to drive the edit page and
   assert the detail page is read-only, plus a hard→soft + description update case
   and a create-with-soft case.
