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
