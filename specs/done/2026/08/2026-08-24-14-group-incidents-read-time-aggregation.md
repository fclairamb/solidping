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

## Implementation Plan

### 1. Delete the group-incident write path (backend)

`server/internal/handlers/incidents/service.go`:

- `routeCheckResultWithIncident` loses `isGroup` entirely — every result goes to
  `handleFailure` / `handleSuccess`, so a grouped check opens a normal per-check
  incident through `createOrReopenIncident` → `createIncident` → `applyRollup`.
- Delete `handleGroupFailure`, `updateGroupMemberOnFailure`, `groupCounts`,
  `createOrReopenGroupIncident`, `createGroupIncident`, `reopenGroupIncident`,
  `handleGroupSuccess`, `resolveGroupIncident`, `formatGroupTitle`,
  `countEnabledGroupMembers`, `publishMemberJoined`, `queueGroupNotifications`
  and the `incident.CheckGroupUID != nil` branch of `queueLifecycleNotifications`.
  Notifications for a grouped check now take the ordinary per-check fan-out,
  including `paging_suppressed` from rollup and the escalation-policy schedule.
- Drop `OnGroupMemberJoined` from the `PublicationHook` interface — the
  "also affecting X" note is now decided inside the publication service itself
  (step 4), not pushed from the incident state machine.
- The READ paths stay untouched: `loadIncidentMembers`, `loadGroupEnrichment`,
  `applyIncidentMembers`, `commentFanoutConnections` and the
  `members` / `checkGroupSlug` response fields still render historical group
  incidents exactly as before. `incident_member_checks` gains no new rows.

### 2. Two DB guards so legacy rows cannot resurrect the group path

- `FindActiveIncidentByCheckUID` (postgres + sqlite): drop the
  `incident_member_checks` OR-branch. No active group incident exists after the
  migration in step 3, and a stale member row must never bind a check to some
  other check's incident again — that binding is precisely defect #1.
- `FindRecentlyResolvedIncidentByCheckUID` (postgres + sqlite): add
  `check_group_uid IS NULL`. Without it, the trigger member of a just-resolved
  legacy group incident would reopen it inside the cooldown and a
  "RabbitMQ — 1/6 checks down" row would come back to life as a per-check
  incident.

### 3. Active group incidents at deploy time — auto-resolve at migration

Chosen over "let them live through their existing lifecycle code path", because
step 1 deletes that code path: keeping it alive would mean keeping every group
function this spec exists to remove. Auto-resolving is also strictly less code
(one SQL section, no Go).

New SECTION appended to the current unreleased consolidated migration
`015_v0_18_0` (both dialects, up + down):

- resolve every active group incident (`state = 2`, `resolved_at = now()`,
  `resolution_type = 'auto'`) and append a note to `description` saying it was
  closed by the per-check-incidents migration;
- clear `currently_failing` on their member rows, so no member check is left
  pointing at a dead incident.

Members that are still down simply open their own per-check incident on the
next failing result. Historical rows are otherwise untouched and still render.

### 4. Consolidate at the publication layer (`incidentpublications`)

- Resolve the group from the **check** (`incidentGroupUID`), not from
  `incident.CheckGroupUID`, in `eligiblePages`, `affectedResourceNames`,
  `affectedName` and `templatedTitle`. Without this a status page that displays
  a group as one resource would stop publishing outages for grouped checks —
  a regression this spec must not introduce.
- `AutoPublish`: when the incident's check is in a group and an active
  auto-created publication already exists on that page for a **sibling**
  incident of the same group, do not mint a second public entry. Post the
  rate-limited "also affecting X" note on the existing one instead. That is the
  consolidated entry: one public incident per group per page, however many
  member checks fail.
- `OnIncidentResolved`: a consolidated publication is not resolved while a
  sibling incident of the same group is still active — the public entry closes
  when the last member recovers.
- Public narrative text stays templated from the resource's public name. The
  literal "N/M checks down" string is deliberately NOT pushed into a public
  field: this package's documented invariant is that nothing internal reaches
  customer-facing text, and member cardinality is internal fleet structure. The
  consolidation (and `affectedResources`) is what the framing buys.

### 5. Read-time aggregation in dash0

Per `## Resolved open questions`: plain grouping headers, grouped client-side,
no API change.

- New `web/dash0/src/lib/incident-grouping.ts` with a pure
  `groupIncidentsByCheckGroup(incidents, checks, groups)` returning an ordered
  list of `{ group, incidents }` rows — ungrouped incidents keep their original
  position, grouped ones collapse under the header of their first member.
- `routes/orgs/$org/incidents.index.tsx`: render a non-interactive header row
  `RabbitMQ — 2/6 down` (N = member incidents in hand and currently active,
  M = enabled checks in the group from `useCheckGroups`) above its member rows.
- `components/dashboard/dashboard-page.tsx`: same header inside
  `ActiveIncidentsList`.
- i18n keys added to all four locales (`en`, `fr`, `de`, `es`).

### 6. Tests

Backend:
- a grouped check going down opens its OWN incident (`check_uid` = that check,
  `check_group_uid` NULL) and writes NO `incident_member_checks` row;
- two members of the same group failing minutes apart produce TWO independent
  incidents;
- a grouped check acts as a rollup PARENT — a dependent's incident is
  `paging_suppressed` and `caused_by_incident_uid` points at it (the defect);
- `GET /incidents?checkUid=<grouped check>` returns that incident;
- a historical group incident still renders with its members;
- the migration resolves an active group incident and clears its member flags;
- publication consolidation: two members of one group produce ONE publication.

Frontend: vitest unit tests for `groupIncidentsByCheckGroup`.

### 7. Docs

`wiki/database-model/results-incidents.md` and
`wiki/features/notifications-and-escalation.md` describe group incidents as a
live mechanism — reword to "historical only". No OpenAPI change: the response
shape is unchanged.
