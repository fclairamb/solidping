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
