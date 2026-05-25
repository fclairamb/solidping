# UID Addressing for Escalation Policies & On-Call Schedules

> Follow-up surfaced by the Terraform Provider API completeness audit
> (`docs/terraform-provider-api-audit.md`, spec `2026-05-25-10-terraform-provider`).

## Context

The Terraform provider models every resource's `id` as the entity `uid`, and
`terraform import` accepts `org/uid` (per `2026-05-25-10`). Read/update/delete must
therefore be reachable by **uid**.

Three of the five v1 resources already satisfy this:

- `checks` — `GET/PATCH/DELETE /checks/:checkUid` resolves uid or slug.
- `channels` — `GET/PATCH/DELETE /channels/:uid` (uid).
- `status-pages` — `GET/PATCH/DELETE /status-pages/:statusPageUid` resolves uid or slug
  (`GetStatusPageByUidOrSlug`).

Two do **not**:

- **`escalation-policies`** — routes are `/:slug` only; handlers call `GetPolicyBySlug`
  (`server/internal/app/server.go:659-661`, `handlers/escalationpolicies/handler.go:233`).
  No by-uid service or DB method exists.
- **`on-call-schedules`** — routes are `/:slug` only; `GetSchedule` calls
  `GetScheduleBySlug` (`server.go:628-630`, `handlers/oncallschedules/handler.go:234`). A
  `GetScheduleByUID` service method exists (`handlers/oncallschedules/service.go:104-118`)
  but is **not wired to any route**.

Result: `terraform import org/uid` cannot fetch these two resource types. Slug also makes a
weak Terraform id because `slug` is mutable via PATCH on both — changing the slug would
orphan the imported resource.

## Goal

Make `escalation-policies` and `on-call-schedules` addressable by **uid** on
GET/PATCH/DELETE, matching the uid-or-slug behavior of `checks` and `status-pages`, so the
provider can use `uid` as the stable Terraform id and `terraform import org/uid` works.

## Non-goals

- Removing slug addressing — keep it; resolve the path param as **uid-or-slug** (same
  pattern as `checks`/`status-pages`), so existing slug-based callers keep working.
- Any schema/data-model change — `uid` already exists on both tables.

## Approach

For each of the two handlers, resolve the existing `:slug` path param as **uid-or-slug**:

1. **DB layer** — add `GetEscalationPolicyByUidOrSlug(ctx, orgUID, identifier)` (escalation
   has no by-uid method today) and reuse the existing `GetScheduleByUID` /
   `GetOnCallScheduleBySlug` for on-call via a small `GetOnCallScheduleByUidOrSlug` helper.
   Detect uid vs slug by UUID-shape (slugs are 3-20 chars, lowercase, no UUID per the slug
   validator) — same heuristic used by checks/status-pages.
2. **Service layer** — add `GetPolicyByUidOrSlug` / `GetScheduleByUidOrSlug` (or extend the
   existing `GetPolicyBySlug` / `GetScheduleBySlug` resolution) and route Update/Delete
   through the same resolver so PATCH and DELETE accept a uid too.
3. **Handlers** — point `GetPolicy`/`UpdatePolicy`/`DeletePolicy` and
   `GetSchedule`/`UpdateSchedule`/`DeleteSchedule` (and the on-call sub-routes that take
   `:slug` — preview, overrides, ical-feed) at the uid-or-slug resolver. The path param can
   stay named `:slug` for backward compatibility, or be renamed to a neutral
   `:scheduleUid` / `:policyUid` — keep it minimal.
4. **Docs** — add the `escalation-policies` and `on-call-schedules` endpoint sections to
   `docs/api-specification.md` (they are currently undocumented), noting uid-or-slug
   addressing.

## Acceptance criteria

- `GET/PATCH/DELETE /orgs/:org/escalation-policies/:id` and
  `GET/PATCH/DELETE /orgs/:org/on-call-schedules/:id` succeed when `:id` is a **uid** and
  still succeed when `:id` is a **slug**.
- A nonexistent uid returns `404 NOT_FOUND` (idempotent delete preserved).
- `docs/api-specification.md` documents both resources' endpoints.
- Table-driven handler/service tests cover uid path, slug path, and 404 for both resources.

## Priority

P2.2 — small, mechanical API task. Unblocks `terraform import org/uid` for the last two
provider resources. Not a blocker for the provider's initial release (it can import these
two by slug in the interim).
