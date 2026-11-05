---
model: sonnet
effort: medium
---

# Changing the regions of many checks needs one edit per check

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/39

## Source

A session replay showed a user spending about 12 minutes opening checks one at a
time, going to Edit, switching placement to "Choose regions" and ticking or
unticking regions, then saving, for many checks in a row. Rage clicks were
recorded on the region checkboxes and region rows of the check form. The report
asks for (1) a region row that toggles exactly once on click, with immediate
feedback, and (2) a bulk action on the checks list to set placement/regions on
several checks at once.

## Problem

- **The toggle itself is not broken.** The region row is a `<label>` wrapping a
  Radix `Checkbox` with `onCheckedChange={() => toggleRegion(region.slug)}`
  (`web/dash0/src/components/shared/check-form.tsx:1904-1925`, `toggleRegion` at
  `:1192`). A jsdom reproduction inside a `<form>` (clicking the region text, the
  checkbox, then the row) counted exactly one `onCheckedChange` per click, and
  `e2e/check-region-spread.spec.ts` clicks the row itself and expects one toggle.
  The rage clicks are the repetition, not a double toggle: the report is wrong
  on (1), right on (2).
- **There is no bulk region change.** The only bulk placement action is "Switch
  to automatic placement" (`POST /api/v1/orgs/:org/checks/auto-placement`,
  `server/internal/app/server.go:1244`, handler
  `server/internal/handlers/checks/handler.go:633`; dialog
  `web/dash0/src/components/checks/auto-placement-bulk.tsx`, opened from the ⋯
  menu of `web/dash0/src/routes/orgs/$org/checks.index.tsx:1663`). It switches
  checks to automatic placement; it cannot pin a chosen region set. The checks
  list has no row selection either.

## Why a spec

- A new API endpoint (bulk pin/set regions), with OpenAPI and docs changes.
- A product/UX decision: how checks are selected for the bulk action.
- Over ~4 files and ~150 lines (endpoint, service, dialog, list selection, tests).

## Proposal

1. **API**: `POST /api/v1/orgs/:org/checks/regions` (literal segment, registered
   with `/auto-placement`, ahead of `/:checkUid`), body
   `{"checkUids": [...], "regions": ["eu-fr-paris", ...], "dryRun": false}`.
   Pins every listed check to exactly `regions` (same semantics as an explicit
   `regions` list on PATCH, see `web/docs/docs/features/region-placement.md` § Pinned
   placement), validating each region exactly as a single-check update does
   (unknown region, private region capability, plan rate limit). Responds with
   per-check results (`updated`, `skipped` with the reason) and the same
   rate-limit warning the single-check update returns. `dryRun` returns the
   counts without writing, like the auto-placement preview. Same permission as
   editing a check. One transaction or per-check, as the auto-placement handler
   does; reuse its code path.
2. **Dashboard**: a "Set regions…" entry next to "Switch to automatic placement"
   in the checks list's ⋯ menu, opening a dialog with the region picker (reuse
   the check form's region list) and the target checks (see open question),
   showing the dry-run count before confirming. `data-testid` on the dialog,
   region options and confirm button.
3. **Docs**: `web/docs/docs/features/region-placement.md` § Switching gains the bulk pin;
   the OpenAPI spec gains the endpoint.
4. **Tests**: Go handler/service tests (pins listed checks only, rejects an
   unknown region, dry run writes nothing, cross-org check UID is refused);
   Playwright: pick two checks, set one region, both detail pages show it, a
   third check is untouched.

## Open questions

1. **How are the target checks chosen?** Recommended: the checks currently
   matched by the list's filters (type, status, group, search), as the dialog's
   scope, with the count shown before confirming. It needs no row-selection UI
   and covers "every HTTP check" style changes. Trade-off: no ad-hoc pick of
   unrelated checks; adding row checkboxes to the list is the alternative, more
   flexible but a larger UI change on an already dense table.

## Closing the issue

This spec closes fclairamb/solidping-business#39. The implementing PR body
**must** carry a `Closes fclairamb/solidping-business#39` line. When the spec is
archived to `specs/done/`, verify the issue is closed and, if the merge did not
close it, close it by hand:

    gh issue close 39 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"

## Resolved open questions

1. (autopilot: recommended answer) Target the checks currently matched by the list's filters (type, status, group, search), as the dialog's scope, with the count shown before confirming. No row-selection UI.
