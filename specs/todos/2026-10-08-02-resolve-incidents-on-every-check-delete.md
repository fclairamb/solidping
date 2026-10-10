---
model: opus
effort: medium
---

# Resolve a check's active incidents on every deletion path, with a `check_deleted` resolution type

GitHub Issue: https://github.com/fclairamb/solidping/issues/493

## Source

The owner found two incidents in production still `state = 1` (active) on checks whose
`deleted_at` is set:

1. A new user's heartbeat check went down, its incident opened and **escalated**, then the
   user deleted the whole organization. The incident stays active and escalated.
2. In the `demo` org, an incident opened at 18:12 on 2026-09-07 is still active on a check
   soft-deleted at 19:32 the same day.

Orphan query from the report:

```sql
select i.uid, i.organization_uid, i.started_at, c.deleted_at
from incidents i join checks c on c.uid = i.check_uid
where i.state = 1 and i.deleted_at is null and c.deleted_at is not null;
```

The report's diagnosis (some paths call `db.DeleteCheck` directly and skip resolution) is
right. Its guess that the demo cleanup is one of them is wrong: that job goes through the
service (see below).

## Problem

- The service path resolves: `checks.Service.DeleteCheck` calls
  `resolveActiveIncidentsForDelete` (`server/internal/handlers/checks/service.go:2725`), then
  `s.db.DeleteCheck` (`:2748`). `resolveActiveIncidentsForDelete` (`:2788-2811`) sets
  `State` and `ResolvedAt` only, **no `resolution_type`**, so the timeline cannot say why the
  incident closed.
- Three callers bypass the service and call the DB layer directly:
  - `server/internal/handlers/auth/org_delete.go:251` (`stopOrgChecks`, `:231-256`): org
    deletion. This is production orphan 1.
  - `server/internal/handlers/testapi/reset.go:44`: test API reset.
  - `server/internal/handlers/testapi/bulk_checks.go:247`: test API bulk delete.
- Already safe (they go through `checks.Service.DeleteCheck`): REST handler
  (`checks/handler.go:602`), apply/prune (`checks/apply.go:466`), private-location monitors
  (`checks/private_location_monitor.go:246,370`), demo cleanup
  (`jobs/jobtypes/job_demo_cleanup.go:171`), MCP (`mcp/tools_checks.go:447`), Slack, Discord
  and Teams chat-ops (`integrations/slack/mention_commands.go:209`,
  `integrations/discord/commands.go:233`, `integrations/msteams/mention_commands.go:178`).
  Orphan 2 on `demo` therefore came from a path not in this list. The implementer must find
  it (candidates: a test-API call against `demo`, an org-level delete, a delete that raced an
  incident opening, i.e. the incident opened by a result that was in flight when the check was
  deleted). Check the logs around 2026-09-07 19:32 if available, and the check worker's
  result path for a missing "check deleted" guard.
- The DB layer `DeleteCheck` (`server/internal/db/postgres/postgres.go:2259`,
  `server/internal/db/sqlite/sqlite.go:2162`) only soft-deletes the check. The invariant
  depends on every caller remembering.
- `models.ResolutionType*` (`server/internal/db/models/incident.go:22-36`) has `auto`,
  `manual`, `expired`, `escalated`, `disabled`, no value for "the check is gone".

## Why a spec

Needs a data backfill for existing orphans (a migration), a new `resolution_type` value, a
change spanning the DB layer, the org-deletion handler, the test API and the escalation/
notification side, and correctness hinges on data mutation and on stopping escalations.

## Proposal

1. **One rule, impossible to bypass.** Resolve active incidents inside the DB-layer
   `DeleteCheck` itself (postgres and sqlite), in the same transaction as the soft delete:
   `UPDATE incidents SET state = 2, resolved_at = now, resolution_type = 'check_deleted'
   WHERE check_uid = ? AND state = 1 AND deleted_at IS NULL`. Then remove
   `resolveActiveIncidentsForDelete` from the service (or keep it only if it does more than
   the DB update, e.g. emits events; in that case the service emits and the DB guarantees).
2. **New resolution type** `ResolutionTypeCheckDeleted = "check_deleted"` in
   `models/incident.go`, with a comment like its siblings. Check for a CHECK constraint or
   enum on `incidents.resolution_type` in both schemas and extend it in the migration.
   Surface it in dash0's incident timeline copy (i18n, all locales) and in the OpenAPI enum
   if `resolutionType` is enumerated there (`make generate`).
3. **Escalation and notifications.** Verify that the escalation scheduler
   (`jobs/jobtypes/job_escalation_step.go`) re-reads incident state before each step and
   stops on a resolved incident. Add the guard if it does not. Decide (see Open questions)
   whether a `check_deleted` resolution sends a "resolved" notification.
4. **Close the race.** If the check worker can open an incident from a result for a check
   that was deleted in the meantime, make incident creation skip deleted checks.
5. **Backfill migration** (postgres + sqlite, per `wiki/conventions/migrations.md`): resolve
   every existing orphan with `resolution_type = 'check_deleted'` and
   `resolved_at = checks.deleted_at`.

### Tests

- Next to `TestDeleteOrgStopsInternalChecksToo`: delete an org that has an active,
  escalated incident; the incident ends `state = 2` with `resolved_at` set and
  `resolution_type = 'check_deleted'`, and no further escalation step fires.
- One test per remaining direct caller: test API reset and bulk delete.
- A DB-layer test (both backends) that `DeleteCheck` resolves active incidents and leaves
  already-resolved ones untouched.
- A migration test: an orphan row is resolved after migrating.

## Open questions

1. **Send a "resolved" notification for a `check_deleted` resolution?**
   Recommended: **no** for org deletion (nobody is left to read it, and the integrations are
   being deleted too); **yes, once, with "check deleted" wording** for a single-check delete,
   so a pager that was alerted is told the incident is closed. Trade-off: one extra
   notification per deleted check that had an open incident, vs. a page that stays open in
   the user's head (and in PagerDuty, if forwarded) forever.

## Closing the issue

This spec closes #493. The implementing PR body **must** carry one `Closes #493` line so the
squash-merge closes it. When the spec is archived to `specs/done/`, verify the issue is
closed and, if the merge did not close it, close it by hand:

    gh issue close 493 --comment "Implemented by <PR or commit>; spec: <archived spec path>"
