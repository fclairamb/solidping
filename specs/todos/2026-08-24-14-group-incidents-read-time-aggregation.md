---
model: opus
effort: xhigh
---

# Group incidents hide member failures and defeat dependency rollup — replace them with per-check incidents aggregated at read time

## Problem

Incidents for checks that belong to a check group are consolidated into a single
"group incident" keyed to whichever member failed **first**
(`incidents.check_uid` = the trigger member). This caused real damage during the
RabbitMQ outage of 2026-08-23 (UTC):

- 23:23:30 — `rabbitmq-aws-nonprod` timed out and opened group incident #366
  ("RabbitMQ — 1/6 checks down", `check_uid` = nonprod).
- 23:48:05 — `rabbitmq-aws-prod` (the core check ~100 dependents point at) went
  down. At 23:50:14 it confirmed and **joined #366 as a member row** instead of
  opening its own incident (`group incident race resolved, joined existing`,
  logged at `server/internal/handlers/incidents/service.go:1547`).
- Consequences:
  1. **The prod check's page shows no incident** — check pages and
     `GET /incidents?checkUid=` filter on `incidents.check_uid`; membership in
     `incident_member_checks` is invisible there.
  2. **Prod going down never generated a fresh page** — it inherited the
     notification/escalation state of a stale nonprod incident that had already
     escalated at 23:27.
  3. **Dependency rollup was blind**: `findRollupRoot`
     (`server/internal/handlers/incidents/rollup.go:111`) matches parent
     incidents by `check_uid` (`server/internal/db/postgres/check_dependencies.go:219-248`),
     so a grouped check can never act as a rollup parent unless it happens to be
     the group's first failing member. 55 dependent incidents paged individually.
  4. The merge was semantically wrong: nonprod (23:23) and prod (23:48) are
     different clusters, 25 minutes apart, plausibly unrelated causes — the
     group incident asserted a correlation that didn't exist.

The consolidation benefit ("one incident instead of up to N per group") is a
presentation concern, and it is achievable at read time without distorting the
data model.

## Proposal

**Incidents are always per-check.** Check groups remain as an
organizational/display concept only.

1. **Delete the group-incident creation path.** In
   `server/internal/handlers/incidents/service.go`: the `isGroup` routing
   (`:879-908`), `createOrReopenGroupIncident` (`:1494`), `createGroupIncident`
   (`:1520`), `reopenGroupIncident`, `updateGroupMemberOnFailure` (`:1382`),
   `groupCounts`, `publishMemberJoined`, and the group branches of the
   resolve path (`:1835` area). A check in a group goes down →
   `createOrReopenIncident` like any other check, including `applyRollup`.
2. **Read-time aggregation.** Dashboard (dash0) incidents list and the
   dashboard home group active incidents by the check's `check_group_uid` and
   render a "RabbitMQ — 2/6 down" style header with the member incidents
   beneath. Status pages / publications produce the consolidated entry at the
   publication layer (this is the one place the "N/M checks down" framing earns
   its keep).
3. **Historical data**: keep existing group-incident rows as-is (they render as
   before); do not migrate or rewrite them. `incident_member_checks` stays for
   history but gains no new rows. Any **currently active** group incident at
   deploy time keeps living through its existing lifecycle code path until
   resolved — or, simpler, is auto-resolved at migration with a note, letting
   per-check incidents reopen naturally. Implementer picks after checking which
   is less code; either is acceptable.
4. **Notifications** follow the normal per-check path, including
   `paging_suppressed` from rollup. Accepted trade-off: a correlated infra
   event (e.g. AWS region outage) taking down prod *and* nonprod members now
   produces one incident per member instead of one merged incident — that is
   deliberate; prod and nonprod deserve distinct paging.

Out of scope: the late-arriving-parent rollup re-evaluation (companion spec
2026-08-24-15).

## Resolved open questions

> Exact UX of the grouped rendering in dash0 (collapsible section vs. plain
> grouping headers) — follow the design reference.

**Decision: plain grouping headers.** Render a non-interactive
`RabbitMQ — 2/6 down` header row with the member incidents listed beneath it.
Do **not** build an expand/collapse affordance: there is no per-group
open/closed state to persist across filters, sorts and pagination, and this
matches the existing list patterns in the design reference
(`web/dash0/src/routes/orgs/$org/design-reference.tsx`). The accepted
trade-off is that a large group occupies more vertical space with no way to
fold it away.

> Whether `GET /incidents` grows a `groupBy=checkGroup` parameter or the client
> groups locally; pick whichever keeps the API conventional.

**Decision: the client groups locally.** `GET /incidents` stays a flat list —
do **not** add a `groupBy` query parameter and do not introduce a second
response shape. dash0 groups the incidents it holds by the check's
`check_group_uid`. This keeps the API conventional: the repo's list endpoints
take filters (`q`, `limit`, `checkUid`) and never a grouping verb.

Accepted trade-off: with pagination a group's members can straddle a page
boundary, so the `N/M down` header reflects the incidents currently loaded
rather than a server-side whole-group total. That is acceptable for v1 — do
**not** add a group-count field to the incident payload to compensate. If the
header would otherwise be misleading, phrase it from the data actually in hand
rather than asserting a fleet-wide count.
