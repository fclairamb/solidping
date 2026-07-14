---
model: sonnet
effort: medium
---

# dash0: fix bogus dependency rendering

## Problem

The check-dependency UI can render in a broken/"bogus" state. The source issue
shows a small screenshot of a malformed dependency display but gives no textual
detail beyond that it looks wrong.

Source issue: [#129 — dash0: bogus dependency rendering](https://github.com/fclairamb/solidping/issues/129)
(the screenshot is the only description; see the issue for the image).

The dependency rendering lives in dash0 under:
- [`web/dash0/src/components/checks/dependencies-card.tsx`](web/dash0/src/components/checks/dependencies-card.tsx)
- [`web/dash0/src/components/checks/form/sections/dependencies.tsx`](web/dash0/src/components/checks/form/sections/dependencies.tsx)
- [`web/dash0/src/components/shared/dependency-cycle-path.tsx`](web/dash0/src/components/shared/dependency-cycle-path.tsx)
- locale strings in `web/dash0/src/locales/*/dependencies.json`

## Proposal

1. **Reproduce first.** Recreate the state in the screenshot — the likely
   candidates are: an empty / single-item dependency list rendering a stray
   connector or label, a missing dependency name falling back to a raw
   uid/placeholder, a broken cycle-path rendering
   (`dependency-cycle-path.tsx`), or a layout overflow. Compare against the
   image on the issue to identify the exact bogus element.
2. Fix the rendering in the responsible component so the dependency display is
   correct for the reproduced case (and degrades cleanly for empty / single /
   cyclic / missing-name cases). Reuse design-reference primitives; keep it
   responsive.
3. Add a focused test (component/Playwright) covering the previously-bogus case
   so it can't regress.

## Open questions

- The issue has no text, only an image — the first implementation step is to open
  the screenshot on [#129](https://github.com/fclairamb/solidping/issues/129),
  pin down exactly which element is malformed and under what data shape, and note
  the reproduction in the spec before coding. If it can't be reproduced from the
  image, capture what was tried and ask for a concrete repro rather than guessing.

## Reproduction (confirmed)

Downloaded the issue's screenshot (309×99 PNG,
`user-attachments/assets/f8a66b9f-c6d4-465d-ab67-e843443f8171`). It shows the
"Depended on by" card section — header, help text — then a bordered row
containing **only** a red "Hard" kind badge, no check name/link before it.

Root cause (confirmed by reading both frontend and backend code):

- `DependedOnBySection`/`DependsOnRow` in
  `web/dash0/src/components/checks/dependencies-card.tsx` render
  `edge.childCheck.name || edge.childCheck.slug` (lines 216, 418) with no
  further fallback. If both are empty strings, the `<Link>` renders no visible
  text, leaving only the `KindBadge` — exactly the screenshot.
- Backend: `server/internal/handlers/checkdependencies/service.go`
  `loadCheckRefs` (184-202) calls `db.GetCheck` per referenced check UID and
  silently `continue`s on `sql.ErrNoRows`, never inserting that UID into the
  map. `buildDependencyResponse` (212-220) then does a bare Go map index
  (`checks[dep.ParentCheckUID]` / `checks[dep.ChildCheckUID]`), which silently
  yields the zero-value `CheckRef{uid:"", slug:"", name:""}` instead of
  omitting/erroring.
- `GetCheck` (`server/internal/db/postgres/postgres.go`) filters
  `deleted_at IS NULL`. Check deletion
  (`server/internal/handlers/checks/service.go` `DeleteCheck`) only **soft**
  deletes (`deleted_at = now`) — it resolves incidents and deletes
  `check_jobs` but never touches `check_dependencies`. The `on delete cascade`
  FK on `check_dependencies.parent_check_uid`/`child_check_uid` therefore
  never fires (the `checks` row is never hard-deleted), and there's no purge
  job. Net effect: deleting check B, where A depends on B (or B depends on A),
  leaves the edge row alive forever; the next time A's dependency list is
  fetched, B's ref resolves to all-empty and renders as a bare kind badge.

## Implementation Plan

### 1. Backend: stop orphaned edges from ever reaching the API response
- `server/internal/handlers/checkdependencies/service.go`: in `ListForCheck`,
  replace the direct `buildDependencyResponse` append with a small
  `resolvedDependencyResponse` wrapper that reports `(resp, ok)` and skip
  appending when `ok` is false (i.e. either endpoint's `CheckRef.UID` is
  empty). Leaves `buildDependencyResponse`/its existing unit test untouched
  (still used as-is by `Create`/`Update`, where both endpoints are always
  freshly resolved).

### 2. Backend: clean up dependency edges when a check is deleted (prevent future orphans)
- Add `DeleteCheckDependenciesForCheck(ctx, checkUID) error` to `db.Service`
  interface (`server/internal/db/service.go`) plus identical Bun
  implementations in `server/internal/db/postgres/check_dependencies.go` and
  `server/internal/db/sqlite/check_dependencies.go`: soft-delete
  `check_dependencies` rows where `parent_check_uid = ? OR child_check_uid = ?`
  and `deleted_at IS NULL`.
- Call it from `checks.Service.DeleteCheck`
  (`server/internal/handlers/checks/service.go`), alongside the existing
  `check_jobs` cleanup, before the final `s.db.DeleteCheck` call.

### 3. Frontend: defend the rendering itself (belt-and-suspenders)
- `web/dash0/src/lib/dependency-graph.ts`: add a small pure
  `resolveCheckRefLabel(ref)` helper — `name || slug || uid || ""` — mirroring
  the fallback chain already used in
  `components/checks/form/sections/dependencies.tsx:59`.
- `dependencies-card.tsx`: use it in `DependsOnRow` and
  `DependedOnBySection`. When the resolved label is empty (fully-unresolved
  ref — defence for any future/legacy orphan that slips past the backend
  filter), render muted placeholder text (`dependencies:unknownCheck`)
  instead of a `<Link>` to an empty uid, so a kind badge is never shown floating
  alone.
- Add `unknownCheck` locale key to `en/fr/de/es` `dependencies.json`.

### 4. Tests
- Backend unit test in `checkdependencies/service_test.go`: extend/add a test
  for the new filtering behaviour (edge with a missing parent or child ref is
  omitted from `ListForCheck`'s output).
- Backend DB-layer test in `internal/db/service_test.go` (`testChecksWithOrgDelete`
  or a sibling): create two edges referencing a check as both parent and
  child, call `DeleteCheckDependenciesForCheck`, assert both are gone from
  `ListCheckDependencyParents`/`Children`.
- Frontend unit test (vitest) for `resolveCheckRefLabel` covering: name
  present; name empty + slug present; name/slug empty + uid present; all
  three empty.
- Playwright E2E (`web/dash0/e2e/check-dependencies.spec.ts`): create two
  checks, link a dependency edge between them via the UI, delete one side,
  and assert the surviving check's dependencies card shows the empty state
  (not a bogus bare-badge row).
