---
model: opus
effort: medium
---

# Scope status-page component update and delete to their section

## Problem
Found by the security scan of 2026-10-12 (finding N3). Severity: high. Any member with role ≥ `user` in any org can rewrite or hard-delete a component (a status-page resource) on another org's page. On SaaS, open signup means anyone: evilco creates its own org and page, then targets acme's. The scan reproduced it on a local test server: the victim's public page showed the attacker's text, and the delete emptied the victim's section.

- **The writes match on the resource UID only.**
  - `UpdateStatusPageResource` and `DeleteStatusPageResource` (`server/internal/db/service.go:1272-1273`) filter on `WHERE uid = ?` alone, in both backends:
    - Postgres: `db/postgres/postgres.go:6194-6231` and `:6234-6241`;
    - SQLite: `db/sqlite/sqlite.go:6158-6195` and `:6198-6205`.
  - The service reaches them through `writeStatusPageResourceRow` and `deleteStatusPageResourceRow` (`handlers/statuspages/memo_writes.go:187-211`), which take no section.
- **The service checks the page and section, but never the resource.** The page and section are resolved inside the caller's org; the resource is never checked against them.
  - **Update.** `UpdateResource` (`statuspages/service.go:2204-2264`) passes the raw `resourceUID` straight to the write (`:2248-2252`).
    - `assertResourceEditable` (`:2182-2198`) deliberately ignores a lookup failure.
    - A target change is resolved by `resolveResourceTarget` inside the attacker's org (`:2235-2246`). So the victim's component can be re-pointed at the attacker's own check, and the victim's public page then shows a status the attacker controls.
    - The re-read at `:2254` fails with `sql: no rows`, so the attacker gets a 500, but the UPDATE is already committed.
  - **Delete.** `DeleteResource` (`:2411-2441`) also ignores the lookup failure (`:2424-2427`), hard-deletes at `:2429-2433`, and answers 204.
- **The UIDs are public.** The unauthenticated page payload carries every resource UID:
  - `computeStatusPageView` calls `loadSectionsWithResources` (`service.go:2595`, `:4343-4390`);
  - which calls `convertResourceToResponse` (`:4663-4675`), whose `UID` field is at `:4665`.
- **Same flaw over MCP.** `update_status_page_resource` and `delete_status_page_resource` call the same service methods (`mcp/tools_statuspages.go:660-720`).
- **Routes.** `PATCH` and `DELETE /orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources/:resourceUid` (`app/server.go:2016-2017`, `orgGroup`).

**Sweep of the rest of the status-page service and DB layer.** Every other write by UID either takes a UID first resolved inside the caller's org and page, or carries a scope predicate in SQL:
- **Pages** (update, delete, kiosk token, custom domain, branding): the page comes from `resolveStatusPage` (`service.go:4246-4260`) or from `GetStatusPageByUidOrSlug(org.UID, …)`. It is resolved before:
  - `writeStatusPageRow` and `deleteStatusPageRow` (`service.go:1561,1668`);
  - the kiosk token writes (`kiosk.go:32-68`);
  - `writeStatusPageCustomDomainRow` (`custom_domain.go:257,281,307-333`);
  - the branding writes (`statuspageassets/service.go:275-295`).
- **Sections.** `UpdateSection` and `DeleteSection` (`service.go:1913-2044`) write the `section.UID` returned by `resolveSection(page.UID, …)` (`:4262-4279`). Both reorders carry a `section_uid` or `status_page_uid` predicate (`postgres.go:6137-6192`).
- **Selector reconciler.** It only writes rows it has just listed from its own section (`selector.go:350-362,425-488,590-612`).
- **Subscribers.**
  - `RemoveSubscriber` uses `GetSubscriber`, which is scoped to the page (`statussubscribers/service.go:443-464`, `postgres/status_page_subscriber.go:20-37`).
  - Endpoint subscriptions and the public subscribe form both check the incident inside the org (`statussubscribers/channels.go:318-338`, `service.go:255-259`).
  - Confirm and unsubscribe look up by secret token, which is global by design.
- **Custom domains.** `GetStatusPageByCustomDomain` is global by design, since it serves host routing. The verification job writes back the page it loaded (`jobs/jobtypes/job_custom_domain_verify.go:145`).

So components are the only gap in this layer.

## Proposal
1. **DB: add the section predicate.**
   - Change the interface to `UpdateStatusPageResource(ctx, sectionUID, uid, update)` and `DeleteStatusPageResource(ctx, sectionUID, uid)` (`db/service.go:1272-1273`).
   - In both backends, add `Where("section_uid = ?", sectionUID)` and return `sql.ErrNoRows` when `RowsAffected() == 0`. `ReorderStatusPageResources` already makes the same check (`postgres.go:6152-6158`, `sqlite.go:6101-6105`).
   - Update the mock in `notifications/slack_test.go:1374-1382`.
2. **Wrappers.**
   - `writeStatusPageResourceRow` and `deleteStatusPageResourceRow` (`memo_writes.go:187-211`) take a `sectionUID` and forward it.
   - The selector call sites pass the section they already hold: `selector.go:357` (`state.section.UID`), and `:435`, `:479`, `:604` (`sectionUID` or `state.section.UID`).
   - The wrapper list `PageMemoWriteWrappers` (`:233-246`) is unchanged, so `TestPageMemoWrites_NoDirectStatusPageWrites` stays green.
3. **Service: look the resource up first, and answer 404 when it is not there.**
   - Add `ErrStatusPageResourceNotFound` next to `ErrStatusPageSectionNotFound` (`service.go:180`).
   - In `UpdateResource` and `DeleteResource`, load `GetStatusPageResource(ctx, section.UID, resourceUID)` before any validation or write, and map `sql.ErrNoRows` to `ErrStatusPageResourceNotFound`.
   - Run `assertResourceEditable` and the "managed by selector" check against the loaded row.
   - The write passes `section.UID`. A write that affects 0 rows also maps to not-found, as defense in depth against a race.
4. **Error mapping.**
   - `handleResourceError` (`handler.go:1036`) maps `ErrStatusPageResourceNotFound` to a 404. Use a new `base.ErrorCodeStatusPageResourceNotFound` (`STATUS_PAGE_RESOURCE_NOT_FOUND`, next to `base.go:51`).
   - The MCP tools already return `errorResult(err.Error())` (`tools_statuspages.go:681-682,719-720`), so they need no change.

## Tests
- `server/internal/handlers/statuspages/resource_tenancy_test.go` (new), table-driven over update and delete.
  - **Setup.** Start from `setupStatusPagesTest` (`service_test.go:21-34`), whose org is acme. Add a second org, `evilco`, with its own page, section and check.
  - **Cross-org regression.** Both cases fail today: the update commits and the delete removes the row.
    - Call `UpdateResource("evilco", ownPage, ownSection, victimResourceUID, …)` with `publicName`, `explanation` and `checkUid` set to evilco's check. It returns `ErrStatusPageResourceNotFound`, and the victim's row is unchanged (`publicName`, `explanation`, `checkUid`).
    - Call `DeleteResource` on the same path. It returns `ErrStatusPageResourceNotFound`, and the victim's `ListStatusPageResources` still lists the row.
  - **Same org, wrong section.** Address a resource of section A through section B of the same page: not found. This proves the section predicate itself, not just the org check.
  - **Positive control.** Updating and deleting the caller's own resource through its own section succeeds, and the result shows in `ListResources`.
- `server/internal/db/service_test.go`: add a `testStatusPageResourceSectionScope` case to `testService`, which runs on both SQLite and Postgres.
  - With a mismatched `sectionUID`, update and delete return `sql.ErrNoRows` and leave the row untouched.
  - With the right section, both succeed.
- **Handler level** (`server/internal/handlers/statuspages/handler_cover_test.go`, or a new handler test): `PATCH` and `DELETE` on a foreign resource answer 404 with `STATUS_PAGE_RESOURCE_NOT_FOUND`, never 500 or 204.
- `server/internal/mcp/tools_statuspages_test.go`: `update_status_page_resource` and `delete_status_page_resource` on a foreign resource return an error and change nothing.
- **Existing tests stay green**: `selector_test.go` (managed rows: `TestDeleteManagedResource_AnswersConflict`, `TestUpdateManagedResource_PositionAnswersConflict`) and `group_resource_test.go` (`TestUpdateResource_SwitchesTargetKind`).

## Related
L9 (2026-10-12) is related but has a different fix. `PATCH /status-updates/:uid` accepts a foreign `incidentUid`: `handlers/statusupdates/service.go:706-713` sets it without the `resolveIncidentCheck` (`:462-487`) that create applies. The fix is an org check in that package, not a DB predicate.
