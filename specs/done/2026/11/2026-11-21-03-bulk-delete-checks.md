---
model: sonnet
effort: high
---

# Select several checks in the dashboard and delete them at once

GitHub Issue: https://github.com/fclairamb/solidping/issues/941

## Source

A user on the feddit.org project post (comment https://feddit.org/comment/16144435,
2026-11-18), filed by Florent: they want a multi-select on the checks list to delete the
preconfigured checks in one go. Florent's analysis comment on the issue settles the design
(below): a bulk delete endpoint next to the bulk update, not `/apply`.

The report is right: there is no user-facing bulk delete and no row selection.

## Problem

- The only bulk delete is the test API, `DELETE /api/v1/test/checks/bulk`
  (`server/internal/app/server.go:2692`, `handlers/testapi/bulk_checks.go`): slug-prefix
  based, test mode only. It must stay so.
- The org routes have a bulk update, `PATCH /api/v1/orgs/:org/checks`
  (`server/internal/app/server.go:1271` → `handlers/checks/handler.go:676`,
  `bulk_update.go`, spec 2026-11-05-01, `MaxBulkUpdateChecks = 1000`, per-check `skipped`
  reasons, `dryRun`), but only a single-check `DELETE /:checkUid` (`server.go:1349`).
- `Service.DeleteCheck` (`handlers/checks/service.go:2923`) holds the per-check rules a bulk
  delete must reuse, not duplicate: demo sessions only delete their own checks
  (`assertDemoMayWriteCheck`), a bastion other checks tunnel through is refused
  (`assertNotUsedAsTunnel`), active incidents are resolved in the same transaction, events
  emitted.
- `/apply` cannot serve the reporter: it only deletes checks carrying its own
  `solidping-managed` label (`checks/apply.go`) and its deletion cap (`DefaultDeletionCap` = 10)
  protects config-as-code users.
- Dashboard: `web/dash0/src/routes/orgs/$org/checks.index.tsx` has no row selection. Its bulk
  dialogs (`components/checks/set-regions-bulk.tsx`, `auto-placement-bulk.tsx`, mounted at
  `checks.index.tsx:1764-1769`) act on the checks matching the current filters
  (`useChecksMatching`, `web/dash0/src/api/hooks.ts`).

## Why a spec

- A new API endpoint (and OpenAPI change).
- Correctness hinging on data deletion and authorization scope.
- Over ~4 files / ~150 lines (backend + dashboard + tests).

## Proposal

1. **API: `DELETE /api/v1/orgs/:org/checks`**, body `{ "checkUids": [...], "dryRun": bool }`,
   mirroring `BulkUpdateChecks` (same `MaxBulkUpdateChecks` cap and `MaxBulkRequestBytes`
   body limit, same 400 errors for an empty or oversized list).
   - Per check, through the same rules as `DeleteCheck` (factor them out so the single and
     bulk paths share one function). A check that cannot be deleted goes to `skipped`
     with a code and a detail, the others are still deleted. Codes: `not_found` (incl. a
     check of another org), `read_only` (demo), `internal`, `managed` (carries the
     `solidping-managed` apply label: the next apply would recreate it), `tunnel_in_use`,
     `error`.
   - Response: `{ "deleted": [uid...], "skipped": [{ "checkUid", "code", "detail" }] }`.
   - `dryRun` evaluates every check and writes nothing, so the dialog shows real counts.
   - Same permission as a single delete (admin/editor). One audit event per deleted check,
     the same events/notifications as the single delete.
   - OpenAPI spec and the API docs updated.
2. **Dashboard (`checks.index.tsx`)**:
   - A checkbox per row (`data-testid="check-row-checkbox"`), a select-all for the visible
     rows (`checks-select-all`); the selection survives pagination.
   - With a selection, a bulk action bar ("N selected": Delete, Set regions, Automatic
     placement, Clear; `checks-bulk-bar`, `checks-bulk-delete-button`,
     `checks-bulk-clear-button`). The existing bulk dialogs use the explicit selection when
     there is one, the filters otherwise. Usable on mobile.
   - Delete opens a dialog that dry-runs first, lists what will be skipped and why, and
     asks to type the count when more than 10 checks are selected
     (`checks-bulk-delete-dialog`, `checks-bulk-delete-confirm`). i18n for every string.
3. Leave `DELETE /api/v1/test/checks/bulk` untouched.
4. **Tests**:
   - Backend table tests (SQLite and Postgres): mixed eligible, demo, managed, internal,
     tunnel and foreign-org checks; `dryRun` writes nothing; permissions (viewer refused);
     cap and empty list rejected.
   - Playwright: select three checks, delete them, they are gone and the others remain;
     select-all then deselect one; the >10 typed confirmation.
5. Changelog entry.

## Closing the issue

This spec closes #941. The implementing PR body **must** carry a `Closes #941` line so the
squash-merge closes it. When the spec is archived to `specs/done/`, verify the issue is
closed and, if the merge did not close it, close it by hand:

    gh issue close 941 --comment "Implemented by <PR or commit>; spec: <archived spec path>"

Then reply to the reporter on feddit.org with the version that ships it.
