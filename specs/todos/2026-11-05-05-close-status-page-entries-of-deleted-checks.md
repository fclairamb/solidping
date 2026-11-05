---
model: opus
effort: medium
pr: batch
automerge: false
---

# Status-page entries of checks deleted before v0.41.0 stay open forever

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/44

## Source

In-app report from the shared demo (server v0.41.0), on a status page's dashboard view: deleting a check should also remove its related "events", demo page included. The screenshot shows the dashboard's "a status page incident is still open" banner: five auto-published entries ("<check> is experiencing issues") are still `investigating` while every linked check is operational. The "events" the reporter means are these status-page incidents (incident publications), not the audit events feed.

The report is right about the symptom and partly outdated about the cause: a check deleted today does close its entries (see below). What stays is the entries of checks deleted before that path existed.

## Problem

- Today's path works: `checks.Service.DeleteCheck` (`server/internal/handlers/checks/service.go:2878`) soft-deletes the check, the DB layer resolves its active incidents with `resolution_type = check_deleted` (`server/internal/db/postgres/postgres.go:2356`, same in SQLite), then `notifyCheckDeletedIncidents` (`service.go:2943`) calls `incidents.Service.NotifyCheckDeletedIncidents` (`server/internal/handlers/incidents/check_deleted.go:22`), which calls `publishResolved` and thus `incidentpublications.Service.OnIncidentResolved` (`server/internal/handlers/incidentpublications/policy.go:324`). The background service used by the demo cleanup job (`server/internal/jobs/jobtypes/job_demo_cleanup.go:171`) got that publication hook on 2026-10-20 (`server/internal/app/server.go:512-519`, #615), shipped in v0.41.0.
- On the public demo page (`GET /api/v1/status-pages/<org>/<slug>/incidents`), the five entries still open are dated 2026-10-13 to 2026-10-20, each with a single update (never human-touched). Their checks were demo-visitor copies deleted by the hourly cleanup before v0.41.0 was deployed. Their incidents are resolved, so `OnIncidentResolved` will never fire for them again: nothing closes them. `PublishIncident`'s comment (`server/internal/handlers/incidentpublications/service.go:903-911`) already notes that this hook is the only auto-close trigger.
- Two cases remain open even with today's code:
  - The page's `auto_resolve` is `if_untouched` and the entry was edited by a person (`policy.go:396-403`): deleting the check posts a "monitoring" update and leaves the entry open, with no recovery that could ever close it. The same goes for `auto_resolve = never`.
  - Consolidated group entries (`publicationCoversActiveSibling`, `policy.go:365`) whose remaining siblings get deleted later.

## Why a spec

- A data backfill: the stale entries already in production databases must be closed by a one-off reconciliation (or a startup/periodic sweep).
- A product decision the reporter didn't settle: close (resolve) the entries, or delete them from the page.

## Proposal

1. **Reconciliation sweep** in `incidentpublications.Service`: `CloseOrphanedPublications(ctx)` lists the publications that are not resolved, have an `incident_uid`, and whose incident is resolved and whose check is soft-deleted (or gone). Group entries close only once every member's check is deleted or its incident resolved, the same rule as `publicationCoversActiveSibling`. Each one is resolved through the same code as `applyResolvePolicy` (resolved update with the page's language template, `status_page.incident.resolved` event, realtime hint, page memo invalidation), but never posts a "monitoring" update: a deleted check will not recover.
2. **Run it** once at startup (idempotent, like `ReconcileCheckJobSchedules`) and from the existing daily maintenance job, if one fits, so entries orphaned by a crash between the soft delete and the notification are caught too.
3. **Check deletion closes touched entries too**: in `NotifyCheckDeletedIncidents` (or a dedicated `OnCheckDeleted` publication hook), resolve the linked publications whatever `auto_resolve` and `human_touched_at` say, with a "resolved: the monitored resource was removed" update. A deletion is final, unlike a recovery that a human may want to confirm.
4. **Tests** (Go, `incidentpublications`): a publication of a resolved incident whose check is deleted is closed by the sweep; positive control: one whose check still exists stays open; a touched entry is resolved on check deletion; a group entry with one surviving down member stays open. SQLite and Postgres.
5. **Docs**: the status-page auto-resolve section of `web/docs/` says that deleting a check resolves its entries.

## Open questions

1. **Resolve or delete the entries?** Recommended: **resolve** them (with a resolved update), as a recovery does. The public history stays truthful and subscribers get a closure. Trade-off: the deleted check's name stays in the page's history; deleting the entries (soft-delete via `softDeletePublicationRow`) erases that, but rewrites a public history visitors may already have seen. For the demo org, which is wiped hourly anyway, deleting would also be acceptable.
2. **Override `auto_resolve = never` on deletion?** Recommended: **yes**. `never` means "a human closes it after a recovery", and after a deletion there is nothing left to watch. Trade-off: an operator who wanted to write their own closing note loses that chance.

## Closing the issue

This spec closes fclairamb/solidping-business#44 (already closed when the spec was filed; the spec file tracks it). The implementing PR body must carry `Closes fclairamb/solidping-business#44`. When the spec is archived to `specs/done/`, verify the issue is closed and, if not, close it by hand:

    gh issue close 44 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"

## Resolved open questions

1. Resolve the entries (with a resolved update), do not delete them. (autopilot: recommended answer)
2. Override `auto_resolve = never` on deletion: yes. (autopilot: recommended answer)
