---
model: opus
effort: high
---

# Status pages can only display individual checks, so multi-check hosts leak internal topology to the public

## Problem

A status page resource is exactly one check: `status_page_resources.check_uid`
is `NOT NULL` with no alternative target
([001_v0_1_0.up.sql:710-748](server/internal/db/postgres/migrations/001_v0_1_0.up.sql:710)),
and the response shape carries a single `checkUid` with one status and one
availability series
([statuspages/service.go:236-247](server/internal/handlers/statuspages/service.go:236)).

For a host probed by TCP + HTTP + TLS + RDP checks, a status page operator must
either publish all four (exposing internal probe topology and quadrupling the
page) or publish one arbitrary check and hope it's representative. Peers
(Better Stack, Uptime Kuma, Instatus) all support a grouped/aggregate public
component; we don't.

Note the precedent already in the schema: `maintenance_window_checks` targets
either a check or a group with an XOR constraint
([001_v0_1_0.up.sql:827-838](server/internal/db/postgres/migrations/001_v0_1_0.up.sql:827)).
This spec applies the same pattern to status page resources.

Prerequisite: spec `2026-08-01-01` (shared group-status rollup helper).

## Proposal

1. **Schema** (new migration, PostgreSQL + SQLite in lockstep):
   - `status_page_resources.check_uid` becomes nullable;
   - add nullable `check_group_uid` referencing `check_groups(uid)`;
   - XOR check constraint: exactly one of `check_uid` / `check_group_uid` set
     (mirror the `maintenance_window_checks` constraint shape);
   - `on delete`: keep parity with the current check-resource behavior when a
     group is deleted (verify what happens to a resource whose check is deleted
     today and match it — don't invent a new orphan policy in passing).

2. **Admin API** ([handlers/statuspages/](server/internal/handlers/statuspages/)):
   create/update resource accepts `checkUid` XOR `checkGroupUid` (validation
   error naming both fields when zero or both are set); list/get responses
   include whichever is set. OpenAPI + `make generate`.

3. **Public rendering semantics** — a group resource displays as **one
   component**:
   - **status**: the rollup from spec `2026-08-01-01` over the group's enabled
     members, mapped to the public status vocabulary the page already uses;
   - **maintenance**: in maintenance if a maintenance window targets the group
     *or* any member check (windows already support group targeting);
   - **availability**: per time bucket, aggregate member availability as the
     **weighted average** of member buckets (sum of successful over sum of
     total), i.e. extend the existing single-check weighted computation
     ([statuspages/service.go:1330-1372](server/internal/handlers/statuspages/service.go:1330))
     across members rather than inventing a second formula. A member with no
     data in a bucket contributes nothing to that bucket (not zero).
   - Members are **not** listed publicly — no expansion, no member names in the
     public payload. The whole point is hiding internal topology; expansion can
     be a later spec if ever wanted.

4. **Dashboard (status page editor)**: the resource picker gains a way to pick
   a check group instead of a check — follow the design reference for the
   selector pattern; label group entries distinctly (e.g. name + "group ·
   N checks"). Editing an existing resource can switch target kind.

5. **status0**: no new component type — a group resource arrives shaped like a
   check resource (name, status, availability series, maintenance flag). Verify
   nothing in status0 assumes a `checkUid` is present in the public payload; if
   it does, drop or null the field cleanly.

6. **Tests**:
   - Go: XOR validation (0 and 2 targets), rollup status on the public
     endpoint for mixed member states, weighted availability across members
     with asymmetric data density, maintenance via group-targeted and via
     member-targeted windows, group deletion behavior;
   - Playwright: create a group resource in the editor, see one component with
     aggregated status on the public page.

7. **Docs** (`web/docs/`): status page section — how group components behave
   (status rules, availability formula, maintenance).

### Out of scope

- Public expansion of a group into member rows.
- Per-member public history or drill-down.
- Changes to sections (they remain presentational containers; a group resource
  lives inside a section like any other resource).

### Open question (decide during implementation, note in the PR)

Whether the public payload should expose the member count (e.g. for a subtle
"aggregated" hint). Default answer: no — strict topology hiding.

## Implementation Plan

Decisions taken up front (recorded here so the diff is readable):

- **Open question — member count is NOT exposed publicly.** The public payload
  for a group resource carries only `publicName`/group name, status,
  `inMaintenance` and the availability series. No member count, no member
  names, no member types. `check.type` is left **empty** for a group resource
  (a `"group"` type string would itself be an "this is aggregated" hint), and
  dash0/status0 already render the type badge conditionally.
- `checkGroupUid` IS carried on the resource object (both admin and public
  responses) — it is an opaque UUID that reveals no topology, and keeping one
  shared `convertResourceToResponse` avoids an admin/public divergence bug.
- **Response-time chart is omitted for group resources.** Interleaving several
  members' p95 series into one chart produces a meaningless plot and leaks
  per-member latency. `availability.responseTimeData` stays empty; the
  availability bar/percentage is the aggregate surface.
- Member set for both status rollup and availability = **enabled, non-deleted**
  member checks, matching `GetCheckGroupStatusCounts` (spec 2026-08-01-01).

### Step 1 — Schema (migration, both dialects)

Append a block to the current release migration pair `008_v0_7_0.up.sql`
(PostgreSQL + SQLite) — that is the repo's convention (one consolidated
migration per release, blocks appended).

PostgreSQL:
- `alter table status_page_resources alter column check_uid drop not null;`
- `add column check_group_uid uuid references check_groups(uid) on delete cascade;`
  — `on delete cascade` mirrors the existing `check_uid` FK exactly. Note both
  checks and check groups are **soft**-deleted in practice, so the cascade is a
  hard-delete backstop only; the live behavior for a soft-deleted target is
  "resource row stays, live info lookup fails, resource renders without status"
  — identical for checks and groups, so no new orphan policy is invented.
- XOR check constraint mirroring `maintenance_window_checks`:
  `check ((check_uid is not null and check_group_uid is null) or (check_uid is null and check_group_uid is not null))`
- Make `status_page_resources_section_check_idx` partial
  (`where check_uid is not null`) and add the twin
  `status_page_resources_section_group_idx ... where check_group_uid is not null`,
  plus a `check_group_uid` lookup index — mirroring `idx_mwc_check`/`idx_mwc_group`.

SQLite: no `ALTER COLUMN`, so rebuild `status_page_resources` with the
established `*_new` + copy + drop + rename pattern (as migration 008 already
does for `agents`), then recreate the indexes.

### Step 2 — Model + DB layer

- `models.StatusPageResource`: `CheckUID *string`, new `CheckGroupUID *string`.
  Add helpers `IsGroup()`.
- `models.StatusPageResourceUpdate`: `CheckUID`/`CheckGroupUID` + `SetTarget bool`
  so a PATCH can switch target kind (writes both columns, one to NULL).
- `NewStatusPageResource` / `NewStatusPageGroupResource` constructors.
- `UpdateStatusPageResource` (pg + sqlite) writes the target pair when
  `SetTarget`.
- New `db.Service` methods (pg + sqlite, both dialect-neutral Bun):
  - `ListCheckUIDsByGroup(ctx, orgUID, groupUID) ([]string, error)` — enabled,
    non-deleted members.
  - `ListMaintenanceWindowsForCheckGroup(ctx, groupUID)` — windows targeting the
    group directly UNION windows targeting any member check.
- Fix the compile fallout: `statusupdates.validateCheckOnPage` must compare
  through the now-nullable pointer.

### Step 3 — Admin API (validation + responses)

- `CreateResourceRequest`: `CheckUID *string` + `CheckGroupUid *string`; exactly
  one must be non-empty, else `ErrResourceTargetInvalid` →
  `VALIDATION_ERROR` naming **both** fields ("exactly one of checkUid or
  checkGroupUid must be set").
- `UpdateResourceRequest`: same two fields, both optional; supplying both is the
  same validation error, supplying one switches the target kind, supplying
  neither leaves the target untouched.
- Group target resolved via `GetCheckGroupByUidOrSlug`; unknown group →
  `ErrCheckGroupNotFound` → 404.
- `StatusPageResourceResponse`: `CheckUID *string` (omitempty) +
  `CheckGroupUID *string` (omitempty).
- MCP `create_status_page_resource` gains an optional `checkGroupUid` arg.
- OpenAPI: `StatusPageResource`, `CreateStatusPageResourceRequest`,
  `UpdateStatusPageResourceRequest` updated (checkUid no longer required), then
  `make generate`.

### Step 4 — Public rendering

- `getGroupInfo(ctx, orgUID, groupUID)` → `ResourceCheckInfo`:
  `Name` = group name, `Type` = "" (see decisions), `Status` =
  `models.RollupGroupStatus(GetCheckGroupStatusCounts()[groupUID])` mapped
  through the same public vocabulary mapping used for checks (validating→up),
  `InMaintenance` = any active window from
  `ListMaintenanceWindowsForCheckGroup`.
- `ViewStatusPage` dispatches per resource: check → `getCheckInfo`, group →
  `getGroupInfo`.
- Availability: build a `resource → []checkUID` expansion map once
  (`resourceCheckUIDs`), collect the union for a single
  `uptimebar.BucketAvailability` call, then for a group resource **merge**
  member `BucketStats` per bucket (`Up += `, `Total += `, `DurCnt += `,
  `DurSum += `). `BucketStats.AvailabilityPct()` then yields exactly the
  weighted average (sum successful / sum total); a member with no rows in a
  bucket simply contributes nothing. Both the daily and the hourly path reuse
  the same merge helper — no second formula.
- Response time: skipped for group resources (decision above).

### Step 5 — dash0 (status page editor)

- `api/hooks.ts`: `StatusPageResource.checkUid?: string`, `checkGroupUid?: string`;
  create/update request types gain `checkGroupUid`.
- New `CheckGroupPicker` (single-select), modelled on the existing
  `CheckPicker` popover pattern from the design reference, listing groups with
  "name · N checks".
- `AddResourceDialog`: a target-kind toggle (check / group) switching between
  `CheckPicker` and `CheckGroupPicker`; exclusion sets per kind.
- `ResourceRow`: tolerate a missing `checkUid` (no check link for a group row),
  show a "group" badge, and label with the group name.
- Editing an existing resource can switch kind: an edit dialog/route sends
  `checkUid` or `checkGroupUid`.
- Add the `CheckGroupPicker` to `design-reference.tsx` so the catalog stays
  canonical.
- i18n strings for all shipped locales.

### Step 6 — status0

Make `StatusPageResource.checkUid` optional in `web/status0/src/api/hooks.ts`
and verify no component dereferences it (grep says only the type declares it).
No new component type.

### Step 7 — Tests

- Go unit/service: XOR validation (0 targets, 2 targets), group rollup status
  for mixed member states, weighted availability across members with asymmetric
  data density, maintenance via a group-targeted window and via a
  member-targeted window, behavior after group deletion.
- Playwright: create a group resource in the status page editor and see one
  aggregated component on the public page.

### Step 8 — Docs

`web/docs/docs/features/status-pages.md`: a "Group components" subsection —
status rollup rules, the weighted-availability formula, maintenance rules, and
the explicit statement that members are never listed publicly.
