---
model: sonnet
effort: high
---

# Publishing a status page for a check takes three separate manual steps and has no entry point from the check itself

## Problem

Getting one check onto a public status page currently requires three distinct
create flows, in the right order, all starting from the status-pages area:

1. Create the page — `web/dash0/src/routes/orgs/$org/status-pages.new.tsx`
   (delegates to `web/dash0/src/components/shared/status-page-form.tsx`),
   which redirects to the detail page…
2. …which greets the user with "No sections yet" (`detail.noSections`,
   `web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx`) and
   an `AddSectionDialog`, because the backend create path adds no section
   (`CreateStatusPage`, `server/internal/handlers/statuspages/service.go:1129`).
3. Only once a section exists does the per-section `AddResourceDialog` (same
   file) let them attach the check.

And there is no path in the other direction at all: the check detail page
(`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`) never mentions
status pages. For the dominant use case — "I have one check, put it on a
page" — every step is a place to get lost. The section concept in particular
is pure ceremony for a first page: nobody wants zero sections, and the
constructors for the whole chain already exist
(`models.NewStatusPageSection` at
`server/internal/db/models/status_page.go:400`, `NewStatusPageResource` at
`:457`).

## Proposal

Collapse the three steps into one create call, and add the missing entry point
from the check.

1. **Backend — default section + optional initial checks.** Extend the
   `CreateStatusPage` request (`statuspages/service.go:1129`):
   - Always create one default section on page creation — name "Services",
     slug `services`, position 0, via the same path the section-create
     endpoint uses (`service.go:1567`). An empty page with a ready section is
     strictly less friction than "No sections yet"; the section stays
     renamable/deletable like any other.
   - Accept an optional `checkUids: []string` body field (camelCase per REST
     conventions). Validate every UID resolves to a check in the org
     (reject the whole request with `VALIDATION_ERROR` naming the offending
     UID otherwise), then create one `NewStatusPageResource` per check in the
     default section, positions in request order.
   - Page + section + resources must land atomically — no half-created page
     if a resource insert fails.
   - Update the OpenAPI spec (`server/internal/app/openapi/openapi.yaml`) for
     the new field.

2. **Frontend — prefillable create form.**
   - `status-pages.new.tsx`: accept a `checkUid` search param, validated via
     `validateSearch` per the URL-state convention (dash0 `CLAUDE.md`). When
     present, the form shows the check as pre-attached (fetch its name, render
     a non-editable chip or line "Will include: <check name>") and passes
     `checkUids: [checkUid]` on submit. Existing redirect-to-detail behavior
     stays — the user now lands on a page that already shows their check.
   - Consider prefilling the page name from the org name when the form is
     reached with a `checkUid` (check what `status-page-form.tsx` defaults
     today; don't fight an existing default).

3. **Frontend — entry point on the check.** On the check detail page
   (`checks.$checkUid.index.tsx`), add a "Publish on a status page" action
   linking to `/orgs/$org/status-pages/new?checkUid=<uid>`. Follow the design
   reference (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) for
   button style/placement; it is a plain link, not a dialog.

4. **Test fallout to expect.** Backend tests and E2E flows that pin "a fresh
   page has zero sections" (e.g. around `detail.noSections`, and the
   section-creation paths in `statuspages/*_test.go` /
   `web/dash0/e2e/`) will need updating — the empty-sections state still
   exists (user deletes all sections) but is no longer the post-create state.

5. **Tests to add.**
   - Backend: create with `checkUids` yields page + "Services" section + the
     resources in order; a foreign-org or unknown UID rejects atomically
     (page must not exist afterwards); create without `checkUids` still gets
     the default section.
   - E2E (Playwright, `web/dash0/e2e/`): from a check detail page, click
     "Publish on a status page", submit the prefilled form, land on the page
     detail showing the check — then open the public page and see the check
     listed.
   - i18n: new strings in all four locales
     (`web/dash0/src/locales/{en,fr,de,es}/`), and `bun run test:unit` for
     locale-key parity.

Out of scope (worth a follow-up spec if wanted): an "add to an *existing*
status page" picker on the check page — v1 always routes to the new-page form,
which is the onboarding case. The onboarding checklist
(spec 2026-08-28-17) links to this flow as its status-page helper.

## Implementation Plan

### Backend

1. **`server/internal/db/models/status_page.go`** — no new model code needed;
   `NewStatusPageSection` / `NewStatusPageResource` already exist and are
   reused as-is.

2. **`server/internal/db/service.go`** (the `db.Service` interface) — add
   `CreateStatusPageWithDefaultSection(ctx, page *models.StatusPage, section
   *models.StatusPageSection, resources []*models.StatusPageResource) error`,
   documented as landing all three atomically in one transaction. Implement in
   both `server/internal/db/postgres/postgres.go` and
   `server/internal/db/sqlite/sqlite.go`, next to the existing
   `CreateStatusPage`, using `s.db.RunInTx(ctx, nil, func(ctx, tx) error {...})`
   with `tx.NewInsert().Model(...)` for the page, then the section, then each
   resource in order — mirroring the existing `MigrateCheckRegionSlug`
   transaction pattern in both `region_migration.go` files. A failure at any
   insert rolls back the whole transaction, so the page never persists
   half-created.

3. **`server/internal/handlers/statuspages/service.go`**:
   - Add `CheckUIDs []string \`json:"checkUids,omitempty"\`` to
     `CreateStatusPageRequest` (around line 867).
   - Add `ErrCheckUIDInvalid = errors.New("checkUids contains an unknown check
     uid")` next to the other sentinel errors (~line 172), following the
     `ErrSettingsUnknownField` pattern: wrap with
     `fmt.Errorf("%w: %s", ErrCheckUIDInvalid, uid)` so the offending UID
     rides in `err.Error()`.
   - In `CreateStatusPage`, after `org` is resolved and before the transactional
     write: for each `req.CheckUIDs` entry (in order), resolve via
     `s.db.GetCheckByUidOrSlug(ctx, org.UID, uid)`; on miss, return the wrapped
     `ErrCheckUIDInvalid` immediately (whole request rejected, nothing written
     yet — page doesn't exist).
   - Build `section := models.NewStatusPageSection(page.UID, "Services",
     "services", 0)` unconditionally, and one
     `models.NewStatusPageResource(section.UID, resolvedCheckUID, i)` per
     resolved check (positions 0..n-1, request order).
   - Replace the current `s.db.CreateStatusPage(ctx, page)` call with
     `s.db.CreateStatusPageWithDefaultSection(ctx, page, section, resources)`.
     Everything after (analytics capture, audit record, custom-domain apply,
     response conversion) stays unchanged — `page` is already fully populated
     by the constructor before insert, same as today.

4. **`server/internal/handlers/statuspages/handler.go`** —
   `handleCreatePageError`: add a case
   `errors.Is(err, ErrCheckUIDInvalid) → WriteValidationError(writer, "Invalid
   check uid", []base.ValidationErrorField{{Name: "checkUids", Message:
   err.Error()}})`.

5. **OpenAPI** (`server/internal/app/openapi/openapi.yaml`) — add `checkUids:
   type: array, items: type: string` (optional) to the status-page create
   request schema; regenerate the Go client (`go generate ./pkg/client/...`)
   if the spec's build step requires it.

### Backend test fallout (existing tests to fix, not weaken)

- `server/internal/handlers/statuspages/group_resource_test.go`
  `seedPageWithSection` helper: after `CreateStatusPage`, list and
  soft-delete the seeded default section before creating the test's own
  "Core" section — mirrors "the user deletes the default section", keeps
  every downstream `view.Sections[0]` assumption in `group_resource_test.go`,
  `badge_test.go`, `overall_status_test.go`, `summary_test.go` valid
  unchanged.
- `service_test.go` `TestReorderSections_RewritesPositionsByUIDOrder`: the
  page now carries 5 sections (default + 4 created), not 4 — fetch the
  default section's UID and include it in `newOrder`/the final length
  assertion.
- `slug_generation_test.go`
  `TestCreateSection_NoRawDatabaseErrorForMissingSlug`: creating a section
  named "Services" now collides with the seeded default's slug and gets
  suffixed `services-2`, not `services` — rename the test section to "Core"
  (unrelated to the regression under test) and assert 2 sections exist
  (`services`, `core`).

### Frontend

6. **`web/dash0/src/api/hooks.ts`** — add `checkUids?: string[];` to
   `CreateStatusPageRequest`.

7. **`web/dash0/src/routes/orgs/$org/status-pages.new.tsx`** — add
   `validateSearch` accepting an optional `checkUid` string (mirrors
   `incidents.index.tsx`). When present: fetch the check via `useCheck(org,
   checkUid, {})`, look up the org's display name via `useAuth().organizations`
   (already loaded, no extra request), pass `initialName={orgName}` and
   `prefilledCheckName={check.data?.name}` to `StatusPageForm`, and include
   `checkUids: [checkUid]` in the `createStatusPage.mutateAsync` payload.
   Existing redirect-to-detail behavior is unchanged.

8. **`web/dash0/src/components/shared/status-page-form.tsx`** — add optional
   props `initialName?: string` and `prefilledCheckName?: string`. Seed
   `name` state from `initialName` when empty (effect mirroring the existing
   slug-derivation effect, guarded so it never overwrites user input). Render
   a non-editable "Will include: <Badge>{name}</Badge>" line
   (`data-testid="status-page-prefilled-check"`) near the top of the Details
   card when `prefilledCheckName` is set. Import `Badge` from
   `@/components/ui/badge`.

9. **`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`** — add a
   "Publish on a status page" toolbar button (same icon-button-with-lg-label
   pattern as "Badges"), `Globe` icon (matches the sidebar's Status Pages
   icon), linking to `/orgs/$org/status-pages/new` with `search={{ checkUid
   }}`, `data-testid="publish-status-page-link"`.

10. **Locales** — add `detail.publishOnStatusPage` to `checks.json` and
    `form.willInclude` to `statusPages.json` in all four of
    `web/dash0/src/locales/{en,fr,de,es}/`, with real (non-machine-placeholder)
    translations.

### New tests

- Backend (`service_test.go` or a new `create_with_checks_test.go`):
  - `CreateStatusPage` with `checkUids` yields the page + a "Services"
    section (slug `services`, position 0) + one resource per check, in
    request order with positions 0..n-1.
  - An unknown or foreign-org UID anywhere in `checkUids` rejects the whole
    request with `ErrCheckUIDInvalid` naming that UID, AND the page does not
    exist afterwards (assert via `GetStatusPageBySlug`/`ListStatusPages`).
  - `CreateStatusPage` without `checkUids` still creates the default
    "Services" section with zero resources.
- E2E (`web/dash0/e2e/status-page-from-check.spec.ts`, new file): from a
  check's detail page, click "Publish on a status page"; the create form
  shows "Will include: <check name>"; submit; land on the page detail with
  the check visible under "Services"; open the public page and confirm the
  check is listed there too.
- Fix `web/dash0/e2e/status-page-group-resource.spec.ts` line ~99: the public
  page now has 2 sections (seeded "Services" + the test's own "Core"), so
  `publicJSON.sections?.[0]?.resources` no longer reliably points at the
  group's section — find the section by `slug === "core"` instead of
  indexing `[0]`.
- i18n: run `bun run test:unit` (from inside `web/dash0`) for locale-key
  parity across all four locales after the new keys land.
