# Escalation policies and on-call schedules carry a slug that serves no purpose — remove it, address both by UID only

## Problem

Escalation policies and on-call schedules each have a `slug` column (unique per
org) alongside their `uid` primary key:

- `server/internal/db/models/escalation_policy.go:29` — `Slug string`
- `server/internal/db/models/on_call_schedule.go` — `Slug` (notnull) + in
  `OnCallScheduleUpdate`

Unlike organizations (slug is in every URL) and checks (slug appears in event
payloads, exports, and public status surfaces), these two slugs are **never
used publicly**:

- All API routes are behind `RequireAuth` — `server/internal/app/server.go:854`
  (escalation policies) and `server.go:823` (on-call schedules).
- No reference from `web/status0`, the status pages handler, badges, or
  subscribers.
- The only public on-call surface is the iCal feed, which is authorized by a
  rotatable secret, not the slug (`server.go:839`).
- Neither resource appears in `server/internal/app/openapi/openapi.yaml`, so no
  documented public API contract exposes the slug.
- MCP tools don't expose either resource.

The slug is pure cost:

- An extra required field at create time and an extra input in the new/edit
  forms.
- Per-org uniqueness to maintain, plus rename semantics to worry about
  (renaming the slug breaks bookmarked URLs anyway).
- Dual lookup paths: `GetPolicyByUidOrSlug`
  (`server/internal/handlers/escalationpolicies/service.go:132`) and
  `GetScheduleByUidOrSlug`
  (`server/internal/handlers/oncallschedules/service.go:141`), backed by
  `GetEscalationPolicyByUidOrSlug` / `GetOnCallScheduleByUidOrSlug` in both DB
  backends.
- A dashboard inconsistency: checks link by `uid`
  (`web/dash0/src/routes/orgs/$org/checks.index.tsx:153`) while these two link
  by slug (`escalation-policies.index.tsx:214`, `on-call.$slug.*` routes).

## Proposal

Drop the slug entirely from both resources; the UID becomes the only
identifier, matching incidents/notifications.

### Backend

1. **Routes** — rename `:slug` to `:uid` for the five escalation-policy routes
   (`server/internal/app/server.go:857-859`) and the eleven on-call routes
   (`server.go:826-835`). Handlers resolve by UID only; slug-shaped
   identifiers now return 404 `NOT_FOUND`.
2. **Services / DB layer** — delete `GetPolicyBySlug`, `GetPolicyByUidOrSlug`,
   `GetScheduleByUidOrSlug` and the `*ByUidOrSlug` / `*BySlug` queries in
   `internal/db/postgres/` and `internal/db/sqlite/`; keep the plain
   `Get…(orgUID, uid)` variants. Remove slug from create/update inputs and
   from the slug-conflict validation paths.
3. **Models** — remove `Slug` from `EscalationPolicy`, `EscalationPolicyUpdate`
   (`escalation_policy.go:29,55`), `OnCallSchedule`, `OnCallScheduleUpdate`,
   and the `NewEscalationPolicy` / `NewOnCallSchedule` constructors.
4. **Migration** — drop the `slug` columns and their `(organization_uid, slug)`
   unique indexes from `escalation_policies` and `on_call_schedules`, in both
   the PostgreSQL and SQLite migration sets (follow the current consolidated
   `NNN_vX_Y_Z` release-migration convention).
5. **API payloads** — remove `slug` from request/response DTOs in
   `internal/handlers/escalationpolicies/handler.go` and
   `internal/handlers/oncallschedules/`.

### Frontend (dash0)

6. Rename route files: `escalation-policies.$slug.tsx` →
   `escalation-policies.$uid.tsx`; `on-call.$slug.tsx`, `on-call.$slug.index.tsx`,
   `on-call.$slug.edit.tsx` → `$uid` equivalents. Update all `Link`
   params (`escalation-policies.index.tsx:214,241`, on-call list,
   `components/dashboard/my-on-call.tsx`, breadcrumbs) to pass `uid`.
7. Remove the slug input from the new/edit forms (the name+slug pair
   component usage in `escalation-policies.new.tsx`, `on-call.new.tsx`, and
   the edit pages) — name only.
8. Regenerate / update the API client types.

### Tests & fixtures

9. Update `server/test/integration/escalation_policies_test.go`, the scenario
   harness fixtures (`server/test/integration/scenario/`), the
   `cmd/scenariodriver` seed, and any dash0 e2e specs that navigate by slug.

## Non-goals

- Checks, organizations, status pages, and auth providers keep their slugs —
  those are load-bearing (public URLs, exports, event payloads).
- No redirect/compat layer for old slug URLs: these pages are behind login and
  self-hosted bookmarks simply re-resolve from the list page. If we later want
  pretty URLs back, slugs can be reintroduced as display-only.
- No transitional slug-fallback release. **Decided 2026-07-11:** a hard 404 on
  old slug-form URLs is acceptable — drop the columns and the slug lookup in
  the same release.

## Implementation Plan

Address escalation policies and on-call schedules by UID only. No slug anywhere.

### Backend

1. **Models** (`server/internal/db/models/`)
   - `escalation_policy.go`: drop `Slug` from `EscalationPolicy` and
     `EscalationPolicyUpdate`; drop the `slug` param from `NewEscalationPolicy`.
   - `on_call_schedule.go`: drop `Slug` from `OnCallSchedule` and
     `OnCallScheduleUpdate`; drop the `slug` param from `NewOnCallSchedule`.

2. **DB interface** (`server/internal/db/service.go`): remove
   `GetEscalationPolicyBySlug`, `GetEscalationPolicyByUidOrSlug`,
   `GetOnCallScheduleBySlug`, `GetOnCallScheduleByUidOrSlug`. Keep plain
   `GetEscalationPolicy(orgUID,uid)` / `GetOnCallSchedule(orgUID,uid)`.

3. **DB backends** (`postgres/` + `sqlite/` escalation.go / on_call.go):
   delete the `*BySlug` and `*ByUidOrSlug` funcs; drop the `update.Slug`
   branch in the Update funcs. Add a `uuid.Parse` short-circuit to
   `GetEscalationPolicy` / `GetOnCallSchedule` (Postgres) so a slug-shaped
   (non-UUID) id returns `sql.ErrNoRows` → 404 instead of a uuid-cast 500
   (mirrors `GetOrgNotification` in `incident_notification.go`). Add the same
   guard on SQLite for parity.

4. **Services**
   - `escalationpolicies/service.go`: drop `Slug` from `CreatePolicyInput` /
     `UpdatePolicyInput`; delete `GetPolicyBySlug`; rename
     `GetPolicyByUidOrSlug` → `GetPolicy` calling `GetEscalationPolicy`;
     `UpdatePolicy`/`DeletePolicy` resolve via `GetEscalationPolicy`; drop the
     `Slug` field from the model-update mapping and the `NewEscalationPolicy`
     call.
   - `oncallschedules/service.go`: drop `Slug` from `CreateScheduleInput` /
     `UpdateScheduleInput`; delete `GetScheduleBySlug` and
     `GetScheduleByUidOrSlug`; callers use the existing `GetScheduleByUID`;
     drop `Slug` from the model-update mapping and the `NewOnCallSchedule` call.

5. **Handlers** (`escalationpolicies/handler.go`, `oncallschedules/handler.go`):
   remove `slug` from every request/response DTO; read `req.Param("uid")`
   instead of `req.Param("slug")`; call the renamed by-uid service methods.

6. **Routes** (`server/internal/app/server.go`): rename the `:slug` path param
   to `:uid` on the 5 escalation-policy routes and the 11 on-call routes.

7. **Migration** `005_v0_4_0` (postgres + sqlite):
   - PG up: `alter table … drop column slug` on both tables (Postgres drops the
     dependent `(organization_uid, slug)` unique constraint automatically).
   - SQLite up: cannot `DROP COLUMN` a UNIQUE-constrained column → rebuild each
     table without slug (create-new / copy / drop / rename / recreate the org
     index), with `PRAGMA foreign_keys=OFF/ON` guards (via `--bun:split`, since
     the parent tables are FK-referenced with cascade) so children survive.
   - Downs (teardown-only, never run in prod): re-add `slug` backfilled from
     `uid` plus the unique index/constraint.

### Frontend (dash0)

8. Rename route files `escalation-policies.$slug.tsx` → `.$uid.tsx`;
   `on-call.$slug{,.index,.edit}.tsx` → `.$uid…`. Update every `Link`/`navigate`
   param from slug to uid (`escalation-policies.index.tsx`, `on-call.index.tsx`,
   `components/dashboard/my-on-call.tsx`, `CommandMenu.tsx`, breadcrumbs).
9. Remove the slug input from the new/edit forms (`escalation-policies.new.tsx`,
   `on-call.new.tsx`, `on-call.$uid.edit.tsx`, the shared form components).
10. Drop `slug` from the api hooks/types (`api/hooks.ts`, generated types);
    regenerate `routeTree.gen.ts`.

### Tests, fixtures, seed, docs

11. Update `server/test/integration/escalation_policies_test.go`, scenario
    fixtures/harness (`server/test/integration/scenario/`), `cmd/scenariodriver`
    seed + scenarios, and both handler `service_test.go` files. Delete the
    `*ByUidOrSlug` db `service_test.go` cases (or convert to by-uid).
    Add a migration test asserting `slug` is gone from both tables.
12. `wiki/api-specification.md`: change the `:id` path params to `:uid` and drop
    the "uid or slug" wording for both resources.
13. Add a CHANGELOG Unreleased entry.
