---
model: sonnet
effort: high
---

# Server settings has no user directory: a super admin cannot list or search users, nor see which orgs they belong to

## Problem

An operator running a SolidPing instance has no way, from the dashboard, to answer
"does alice@acme.com have an account, and which organizations is she in?". The
server settings area (`/orgs/$org/server/*`, gated to super admins by the
layout at `web/dash0/src/routes/orgs/$org/server.tsx:40`) lists orgs via the
Entitlements tab, but there is no user-level view at all. Membership is only
visible from *inside* an org (`/orgs/$org/organization/members`), which means
the operator has to guess the org first, and cannot see accounts that belong
to no org, or to several.

On the API side the situation is the same:

- `ListUsers` (`server/internal/db/postgres/postgres.go:782`,
  `server/internal/db/sqlite/sqlite.go:708`) is unpaged, has no search, and
  its only consumer is the operator-notifications job
  (`server/internal/handlers/system/operator_notifications.go:86`). It is not
  exposed over HTTP.
- `ListMembersByUser` (`server/internal/db/postgres/postgres.go:982`,
  `server/internal/db/sqlite/sqlite.go:908`) exists but is per-user, so
  building a directory on top of it means one query per row.
- Every `/system/*` route is registered behind `RequireAuth` +
  `RequireSuperAdmin` (`server/internal/app/server.go:1037-1044`,
  `server/internal/middleware/auth.go:704`), so the gating primitive is
  already there. Nothing uses it for users.

The closest analog is the super-admin org list `GET /api/v1/system/entitlements`
(`server/internal/handlers/entitlements/admin.go:110`) and its page
`web/dash0/src/routes/orgs/$org/server.entitlements.index.tsx`: search box,
paged table, `PermissionDenied` on 403. That is the shape to copy. What should
*not* be copied is its in-memory search over `ListOrganizations`
(`admin.go:116-127`): the users table can be much larger than the orgs table,
so search and paging must happen in SQL.

## Proposal

### Backend

**DB layer** — add to the `Service` interface (`server/internal/db/service.go`,
next to `ListUsers` at line 153) and implement in *both* dialects
(`postgres.go`, `sqlite.go`; see the `sync-pg-to-sqlite` skill):

- `SearchUsers(ctx, filter models.UserSearchFilter) ([]*models.User, int, error)`
  with `Query`, `Limit`, `Offset`. Returns the page and the total count of
  matches. `deleted_at IS NULL` always. When `Query` is non-empty, match
  case-insensitive substring on `email` OR `name` (`lower(email) LIKE ?` /
  `lower(name) LIKE ?` with `%` and `_` escaped in the input — the existing
  `users_email_idx` on `lower(email)` at
  `server/internal/db/postgres/migrations/001_v0_1_0.up.sql:59` won't serve a
  `%q%` scan, which is acceptable at this scale; no new index in v1). Order
  `created_at DESC`, then `uid` for a stable page.
- `ListMembersByUsers(ctx, userUIDs []string) ([]*models.OrganizationMember, error)`
  — one query with `Relation("Organization")`, `user_uid IN (...)`,
  membership `deleted_at IS NULL`, and the joined org's `deleted_at IS NULL`
  (`server/internal/db/models/organization.go:25`). This is what keeps the
  handler at two queries per page instead of `1 + limit`.

**Handler** — `GET /api/v1/system/users` in the `system` handler package
(`server/internal/handlers/system/`, new file `users.go`), registered in
`server/internal/app/server.go` as its own group
`api.NewGroup("/system/users").Use(authMiddleware.RequireAuth, authMiddleware.RequireSuperAdmin)`
alongside the `/system` jobs group at line 1037. Query parameters mirror the
entitlements admin list: `q`, `limit` (1–200, default 50), `offset`.

Response is `{ "data": [...], "total": <n> }`. Each row is an **explicit
allow-list DTO**, never the `models.User` struct
(`server/internal/db/models/auth.go:56`), because that struct carries
`PasswordHash`, `TOTPSecret` and `TOTPRecoveryCodes`:

```json
{
  "uid": "…",
  "email": "alice@acme.com",
  "name": "alice",
  "avatarUrl": "",
  "superAdmin": false,
  "demo": false,
  "emailVerified": true,
  "totpEnabled": false,
  "mustChangePassword": false,
  "hasPassword": true,
  "lastActiveAt": "2026-09-18T10:12:00Z",
  "createdAt": "2026-08-01T09:00:00Z",
  "orgs": [
    { "uid": "…", "slug": "acmetech", "name": "Acme", "role": "admin", "joinedAt": "…" }
  ]
}
```

`hasPassword` is `PasswordHash != nil` — it tells the operator whether the
account is SSO/OAuth-only. `role` is the `MemberRole` string
(`server/internal/db/models/auth.go:231-234`). `signupAttribution` stays out.

**Docs** — add the path and its two schemas (`AdminUserRow`,
`AdminUsersListResponse`) to `server/internal/app/openapi/openapi.yaml` next to
the `/api/v1/system/entitlements` block (line 7910), and a `## Users` section
to `wiki/api-specification/system.md`.

### Frontend

- **Tab**: add `{ label: t("tabs.users"), path: "/orgs/$org/server/users" }` to
  the list in `web/dash0/src/routes/orgs/$org/server.tsx:17-32`, placed after
  Entitlements. The layout already redirects non-super-admins, so no extra
  gate is needed on the page — but the page still renders `PermissionDenied`
  on an API 403 (never redirect), as the entitlements page does.
- **Page**: new `web/dash0/src/routes/orgs/$org/server.users.tsx` modelled on
  `server.entitlements.index.tsx`: debounced search input (`useDebounce`,
  300 ms, `data-testid="users-search"`), a table (`data-testid="users-table"`,
  one `users-row-{uid}` per row) with columns Email, Name, Organizations,
  Flags, Last active, Created. Organizations render as one `Badge` per
  membership (`slug · role`), each linking to the org's entitlements detail
  page `/orgs/$org/server/entitlements/$targetOrg` (super-admin reachable
  regardless of membership). Flags are small badges: Super admin, 2FA,
  Demo, Unverified, Password reset pending, SSO only. Dates use `TimeAgo`.
  Paging: Previous / Next driven by `offset`, plus a "showing x–y of total"
  line. Empty and error states like the entitlements page. Wide table
  scrolls inside `overflow-x-auto`; the page must stay usable on mobile.
- **Hook**: `useAdminUsersList({ q, limit, offset })` in
  `web/dash0/src/api/hooks.ts`, next to `useAdminEntitlementsList`
  (line 6538), with an exported `AdminUserRow` type.
- **Locales**: `tabs.users` and a `users.*` block in `server.json` for **all
  four** locales (`web/dash0/src/locales/{en,fr,de,es}/server.json`);
  `locale-parity.test.ts` fails otherwise, so `bun run test:unit` is part of
  the gate.
- **Design reference**: reuse `Input`, `Table`, `Badge`, `Card`, `TimeAgo`
  from `web/dash0/src/routes/orgs/$org/design-reference.tsx`. No new primitive
  is expected; if one is needed, add it there in the same change.

### Tests

Backend (`server/internal/handlers/system/users_test.go` + DB tests in both
dialect packages, so `make test` and `make test-postgres` both cover it):

- 401 anonymous; 403 for an org **admin** who is not super admin (positive
  control: the same request as super admin is 200).
- `q` matches email substring case-insensitively, matches name, and a `%` in
  `q` is treated literally.
- `limit`/`offset` slice a seeded set correctly and `total` reports the
  unpaged match count; `limit` above 200 is clamped.
- A soft-deleted user is absent; a soft-deleted membership and a membership
  in a soft-deleted org are absent from `orgs`; a user with no org has
  `orgs: []`.
- The raw response JSON contains none of `passwordHash`, `totpSecret`,
  `totpRecoveryCodes` (positive control: it does contain `email`).
- `ListMembersByUsers` with an empty slice returns empty without querying.

E2E (`web/dash0/e2e/server-users.spec.ts`, following
`server-entitlements.spec.ts`): the Users tab is visible to the test-mode
super admin, the table lists `test@test.com` with an org badge `test`,
typing a non-matching query empties the table, and clearing it restores the
row.

### Non-goals (follow-up specs if wanted)

- Any write action: promoting to super admin, forcing a password reset,
  deleting or impersonating a user. This spec is read-only.
- Filtering by org, role or flag (`?superAdmin=true`); CSV export.
- Surfacing `signupAttribution`.

### Open questions

- Should the org badge link to the entitlements detail page (proposed, always
  reachable) or to the org's members page (more useful, but relies on the
  super-admin bypass in `RequireOrgAccess` at
  `server/internal/middleware/auth.go:676`)? Default to entitlements unless
  the implementer confirms the bypass holds for that route.

## Implementation Plan

1. **DB layer**: `models.UserSearchFilter` (auth.go); `Service.SearchUsers` and
   `Service.ListMembersByUsers` added to the `db.Service` interface and
   implemented in both `postgres.go` and `sqlite.go`, reusing each dialect's
   existing `escapeLikePrefix` helper for the substring search and the
   `Relation("Organization", apply)` closure to drop memberships whose org is
   soft-deleted.
2. **Handler**: `system.Service.SearchUsers` (business logic: call the two DB
   methods, build the allow-list DTO) + `system.Handler.ListUsers` (HTTP
   parsing: `q`, `limit`, `offset`) in a new `server/internal/handlers/system/users.go`;
   route registered as its own `/system/users` group in `server.go` next to
   the existing `/system` groups.
3. **Backend tests**: `server/internal/handlers/system/users_test.go` (401/403/200,
   search, paging/clamping, soft-delete exclusions, no-org case, JSON
   allow-list assertion) + `SearchUsers`/`ListMembersByUsers` DB tests in both
   `postgres` and `sqlite` packages.
4. **OpenAPI + wiki docs**: `AdminUserRow`/`AdminUsersListResponse` schemas and
   the `/api/v1/system/users` path in `openapi.yaml`; a `## Users` section in
   `wiki/api-specification/system.md`.
5. **Frontend**: `useAdminUsersList` hook + `AdminUserRow` type in `hooks.ts`;
   `server.users.tsx` page modelled on `server.entitlements.index.tsx`; tab
   entry in `server.tsx`; locale keys in all four `server.json` files.
6. **E2E**: author `web/dash0/e2e/server-users.spec.ts`.
7. **QA gate**: `make build-backend lint-back test`, `make build-dash0`,
   `bun run lint`, `bun run test:unit`.
