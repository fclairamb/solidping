---
model: opus
effort: high
---

# Add an `owner` role: org creators become owners, and only an owner can delete the organization

## Problem

The role model has three tiers — `admin`, `user`, `viewer`
(`MemberRole` constants in `server/internal/db/models/auth.go:149-157`) — and
two gaps around organization lifecycle:

1. **No owner concept.** Any authenticated user can already create an org
   (`POST /api/v1/orgs`, wired at `server/internal/app/server.go:559`,
   service `server/internal/handlers/auth/service.go:2629`), but the creator
   is made a plain `admin` (`service.go:2662`). Once they promote other
   members to admin, the creator is indistinguishable from them — nobody
   "owns" the org, and admins can demote or remove the creator.
2. **No way to delete an organization.** There is no `DELETE /api/v1/orgs/:org`
   route at all — only the internal db-layer `DeleteOrganization`
   (`server/internal/db/service.go:75`, implemented for both Postgres and
   SQLite) used by tests. An org, once created, lives forever.

## Proposal

### 1. New `owner` role (backend)

- Add `MemberRoleOwner MemberRole = "owner"` next to the existing constants in
  `server/internal/db/models/auth.go`. Hierarchy: **owner > admin > user > viewer**;
  owner passes every admin gate.
- `CreateOrg` assigns `MemberRoleOwner` to the creator instead of admin
  (`server/internal/handlers/auth/service.go:2662`), and the minted org-scoped
  access token carries the `owner` role claim (`service.go:2697`).
- Middleware: `RequireOrgAdmin` (`server/internal/middleware/auth.go:453`,
  check at `:478`) must accept owner too. Audit any other
  `Role == MemberRoleAdmin` / `Role != MemberRoleAdmin` comparison and switch
  to a small role-hierarchy helper (e.g. `role.AtLeast(MemberRoleAdmin)`)
  rather than sprinkling `|| owner` everywhere.
- Members service (`server/internal/handlers/members/service.go`):
  - `isValidRole` (`:330`) accepts `owner`.
  - **Only an owner may grant or revoke the owner role**; admins cannot
    modify or remove a member whose role is owner.
  - Last-owner guard mirroring the existing last-admin guard
    (`ErrCannotRemoveLastAdmin`, `checkLastAdmin` at `:342`,
    `isDemotingAdmin` at `:337`): the last owner cannot be demoted or
    removed. Multiple owners are allowed (that's also the ownership-transfer
    path: promote someone to owner, then demote yourself).
  - The last-admin guard should keep working when the org has an owner but no
    admins — re-express both guards against the hierarchy (an owner counts as
    an admin for `CountAdminsByOrg`-style checks, or adjust the queries).
- **Migration/backfill**: every existing org must end up with exactly one
  owner — promote its oldest admin (earliest `created_at` in
  `organization_members`) to owner. Covers the seeded `default`/`test` orgs
  too. Ship as a normal SQL migration in both
  `server/internal/db/postgres/migrations/` and
  `server/internal/db/sqlite/migrations/`.
- OAuth/Slack/Discord `findOrCreateOrganization` paths
  (`server/internal/handlers/auth/slack_service.go:385`,
  `discord_service.go:363`, and the generic OAuth org-creation flow) must
  also make the creating user an owner when they create the org.

### 2. Any user can create an org

Already true — `POST /api/v1/orgs` only requires authentication. The change
here is just that the creator now becomes **owner** (above). Verify no
entitlement/config gate is silently expected, and update the OpenAPI spec
(`server/internal/app/openapi/openapi.yaml`) + `wiki/api-specification/` for
the role semantics.

### 3. Org deletion (owner only)

- New endpoint `DELETE /api/v1/orgs/:org` (owner-gated via a new
  `RequireOrgOwner` middleware next to `RequireOrgAdmin`).
- Require explicit confirmation: the request body must carry the org slug
  (`{"slug": "<org-slug>"}`); mismatch → `VALIDATION_ERROR`. Deleting the
  seeded `default` org should be allowed like any other — no special case.
- Soft-delete the organization (`deleted_at`, consistent with the rest of the
  schema) and its memberships; revoke org-scoped refresh tokens so remaining
  sessions die. Scheduled checks for the org must stop running (verify the
  scheduler filters soft-deleted orgs; fix if not). Status pages of a deleted
  org must return 404.
- Response: 204. The caller's own org-scoped session is now invalid — the
  dashboard should drop to the org-switcher / create-org flow.

### 4. Dashboard (dash0)

- Members page (`web/dash0/src/routes/orgs/$org/organization.members.tsx`):
  show the owner role (badge + `members.role.owner` i18n key), sort owners
  first (`:61`), and only offer the "Owner" option in the role select
  (`:233`) when the current user is an owner. Admins see owner rows as
  read-only.
- Org settings (`web/dash0/src/routes/orgs/$org/organization.settings.tsx`):
  add a **danger zone** visible to owners only — "Delete organization" with a
  type-the-slug confirmation dialog, `variant="destructive"` + `Trash2` per
  the frontend conventions. On success, navigate out of the deleted org
  (org switcher or create-org page).
- Follow the design reference for any new primitives.

### 5. Tests

- Backend: role-hierarchy unit tests; members-service guard tests (admin
  can't touch owner, last-owner protection, owner promote/demote); CreateOrg
  assigns owner; DELETE org happy path + non-owner 403 + wrong-slug 400 +
  token revocation; migration backfill test (oldest admin becomes owner).
- E2E (`web/dash0/e2e/`): create org → creator sees owner badge; owner
  deletes org via danger zone and lands outside the org; admin does not see
  the danger zone.

## Open questions

- Should org deletion be reversible (soft-delete restore window) or is
  soft-delete purely an implementation detail? Spec assumes no restore UI.
- Slug reuse after deletion: soft-deleted orgs keep their slug row; decide
  whether `CreateOrg`'s slug-availability check frees the slug (suggest: keep
  slug blocked, matching current `GetOrganizationBySlug` behavior).

## Resolved open questions

**Q: "Should org deletion be reversible (soft-delete restore window) or is
soft-delete purely an implementation detail? Spec assumes no restore UI."**

**Decision: no restore UI — the assumption stands.** Soft-delete remains an
implementation detail. A deleted org disappears from every surface; recovery is
a manual database intervention and is explicitly out of scope. Do not build a
restore listing, a restore action, or an expiry job.

**Q: "Slug reuse after deletion: soft-deleted orgs keep their slug row; decide
whether `CreateOrg`'s slug-availability check frees the slug."**

**Decision: free the slug, and a deleted org's slug must 404 immediately.**
Deletion is deletion: once an org is deleted, every `/dash0/orgs/<slug>/...`,
`/status0/<slug>/<page>` and `/api/v1/orgs/<slug>/...` request for that slug
404s straight away — the deleted org must NOT keep serving status pages, badges,
widgets or API responses. Release the slug so a future `CreateOrg` may claim it:
`CreateOrg`'s availability check must ignore soft-deleted orgs. Do **not** build
any alias, tombstone or redirect for *deleted* orgs.

Note for the implementer: keeping an old slug alive as an alias **is** wanted,
but only for **renamed** orgs, not deleted ones — that is spec
`2026-08-08-12-org-profile-name-slug-logo.md`, which owns the previous-slug
store. If both specs land, a deleted org's slug is freed with no alias, while a
renamed org's previous slug still resolves. Keep the two paths distinct and do
not let a deleted org resolve through the rename-alias mechanism.

## Implementation Plan

### 1. Role hierarchy (`server/internal/db/models/auth.go`)
- Add `MemberRoleOwner MemberRole = "owner"`.
- Add `MemberRole.Rank()`, `MemberRole.AtLeast(min)`, `MemberRole.IsValid()` —
  ranking `owner(4) > admin(3) > user(2) > viewer(1)`, unknown = 0.
- Add `auth.Claims.HasOrgRole(min)` so JWT-claim gates (`claims.Role == "admin"`)
  become hierarchy-aware without sprinkling `|| "owner"`.

### 2. Migration `011_owner_role` (both dialects)
- Widen the `organization_members.role` CHECK to include `'owner'`
  (Postgres: drop/re-add constraint; SQLite: `_new` table rebuild, the established pattern).
- Backfill: for every org that has at least one admin and no owner, promote the
  oldest (`created_at ASC`) live admin to `owner`. Covers seeded `default`/`test`.
- `.down.sql`: demote owners back to admin and restore the narrow CHECK.

### 3. Role gates (audit every comparison)
- `middleware.RequireOrgAdmin` → `member.Role.AtLeast(MemberRoleAdmin)`.
- New `middleware.RequireOrgOwner` (super-admin bypass, same shape).
- Admin-notification fan-outs (`job_escalation_step.go`, `membership_requests.go`,
  `incidentnotifications/handler.go`) → `AtLeast(admin)`.
- Claim gates (`auth/handler.go`, `membership_requests_handler.go`, `discovery`,
  `integrations`, `entitlements`) → `claims.HasOrgRole(admin)`.
- `CountAdminsByOrg` counts `role IN ('admin','owner')` in both dialects; add
  `CountOwnersByOrg`.

### 4. Creator becomes owner
- `auth.Service.CreateOrg`: membership role `owner`, minted access token claim `owner`.
  Slug availability already ignores soft-deleted orgs (`GetOrganizationBySlug` filters
  `deleted_at IS NULL`, and both dialects' unique slug index is partial) — assert with a test.
- `join_policy.go` rule 2 (zero-member bootstrap) mints `owner`; update `join_policy_test.go`.
  This is what makes the Slack/Discord/OAuth `findOrCreateOrganization` paths produce an
  owner, since they all funnel through `CompleteOrgLogin`.
- `job_startup.go` seeded default org admin → owner.

### 5. Members service guards (`handlers/members/`)
- `isValidRole` and `AddMember` accept `owner`.
- Caller role is resolved in the handler (membership, or super-admin) and passed to
  `AddMember` / `UpdateMember` / `RemoveMember`:
  - only a caller at owner level may grant `owner` or touch a member who *is* owner;
  - otherwise `ErrOwnerRoleRequired` → 403 FORBIDDEN.
- Last-owner guard: demoting or removing the last owner → `ErrCannotDemoteLastOwner` /
  `ErrCannotRemoveLastOwner` (409 CONFLICT). Multiple owners allowed → transfer path.
- Last-admin guard re-expressed against the hierarchy (owner counts as admin), so an org
  with an owner and no plain admins is unaffected.

### 6. Org deletion
- `DELETE /api/v1/orgs/:org`, gated by `RequireAuth + RequireOrgAccess + RequireOrgOwner`.
- Body `{"slug": "<org-slug>"}`; mismatch → `VALIDATION_ERROR`. No special case for `default`.
- Service `DeleteOrg`: hard-delete the org's `check_jobs` (so the scheduler stops
  immediately — it does not join organizations), soft-delete its checks, soft-delete its
  memberships, revoke every org-scoped user token, then soft-delete the org. 204.
- Deleted slug 404s everywhere for free: every org resolution (dashboard API,
  status pages, badges, embed widget data) goes through `GetOrganizationBySlug`, which
  filters `deleted_at IS NULL`. Prove it with tests rather than adding new filtering.
- New db method `DeleteUserTokensByOrg` (both dialects).

### 7. dash0
- `MemberRole` type += `owner`; `ROLE_ORDER` sorts owners first.
- Members page: owner badge/label, `Owner` select option only for an owner caller,
  owner rows read-only (role select + remove disabled) for non-owners.
- Settings page: owner-only **Danger zone** card — `Trash2` + `variant="destructive"`,
  type-the-slug confirm dialog; on success clear the session org and navigate out.
- `AuthContext`: `isOwner`; `isAdmin` true for owner too.
- i18n en/fr/de/es.

### 8. Docs & tests
- `openapi.yaml` (role enums + DELETE /orgs/{org}), `wiki/api-specification/`.
- Backend table-driven tests: hierarchy, member guards, CreateOrg→owner,
  bootstrap→owner, delete happy path / non-owner 403 / wrong slug 400 / token revocation /
  scheduler stop / slug reuse / deleted-org 404 on API + status page + badge.
- E2E: owner badge after org creation, danger zone visible to owner only, delete lands
  outside the org.
