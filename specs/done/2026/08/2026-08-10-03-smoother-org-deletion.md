---
model: opus
effort: high
---

# Smoother org deletion — stay signed in, land somewhere sensible (GitHub issue #206)

## Problem

[Issue #206](https://github.com/fclairamb/solidping/issues/206): when you delete an
org it disconnects you and gives you an error when you reconnect. You should stay
connected like when you don't have any org, or be reconnected to the first org it
finds.

What actually happens today:

1. **The backend kills the caller's own session.** `DeleteOrg`
   (`server/internal/handlers/auth/org_delete.go:54-97`) soft-deletes every
   membership *including the caller's*, then `DeleteUserTokensByOrg` (:76) revokes
   **every** refresh token scoped to the org — the caller's included. The handler
   (`server/internal/handlers/auth/handler.go:705-738`) then calls
   `clearAuthCookie` and returns 204. Revoking *other* members' sessions is
   deliberate (spec `2026-08-08-11-owner-role-org-create-delete.md`), but nothing
   preserves the deleter's own authentication.
2. **`/auth/me` 401s on a vanished org slug.** The surviving access token still
   carries `claims.OrgSlug` of the deleted org; `GetUserInfo`
   (`server/internal/handlers/auth/service.go:1379-1397`) resolves it, gets
   `ErrOrganizationNotFound`, and `handleUserInfoError`
   (`handler.go:462-464`) maps that to **401** — so even a plain reload destroys
   the session. The *empty* org-slug case is already handled gracefully via
   `getUserInfoNoOrg` (`service.go:1461-1504`); the deleted-org case just never
   routes there.
3. **The frontend logs out on purpose, then errors on reconnect.**
   `handleDeleteOrg` (`web/dash0/src/routes/orgs/$org/organization.settings.tsx:159-173`)
   calls `logout()` (clearing the token *and* `localStorage.solidping_org`) and
   navigates to `/login`, which falls back to the hardcoded `"default"` org
   (`web/dash0/src/routes/login.tsx:29`). On installs without a `default` org the
   user lands on a login page for a nonexistent org, where the SSO buttons hit
   `/api/v1/auth/<provider>/login?org=<dead>` and render a raw 404 JSON
   `Organization not found` (`server/internal/handlers/auth/google.go:39-41` and
   siblings) — the literal "error when you reconnect".
4. **A race can also surface "session expired".** In-flight requests or the live
   WebSocket racing the delete escalate through `refreshWithOutcome()`
   (`web/dash0/src/lib/token-refresh.ts:61-84`) — the refresh row is gone — and
   `redirectToExpiredLogin()` (`web/dash0/src/api/client.ts:151-158`) hard-navigates
   to `/orgs/<deleted>/login?session_expired=true`, beating the intentional
   `navigate("/login")`.

## Proposal

Desired behavior: after deleting an org, the owner **stays signed in**. If they
have other orgs, they land on the first remaining one; if not, they land on the
existing `/no-org` empty-state page (`web/dash0/src/routes/no-org.tsx`) exactly as
if they had just logged in with zero orgs.

### 1. Backend: return a fresh session instead of killing the caller's

- In `DeleteOrg`, keep revoking all org-scoped tokens (other members' sessions must
  die), but after the delete, mint a **replacement session for the caller** instead
  of `clearAuthCookie` + 204:
  - Re-resolve the caller's remaining memberships. If ≥1 org remains, mint a new
    org-scoped session for the first one (reuse the `SwitchOrg` machinery,
    `service.go:1772-1830`). If none remain, mint an org-less access token via the
    `completeLogin` `resolvedOrg == nil` branch (`service.go:621-635`).
  - Return **200 with the same session payload shape as `SwitchOrg`/login**
    (access token + org context) instead of 204, mirroring the org-rename path
    (`handler.go:740-745`) that already re-issues tokens after a slug change.
- Make `/auth/me` resilient to a vanished org slug: in `GetUserInfo`
  (`service.go:1390-1397`), when the claims' org no longer exists, fall through to
  `getUserInfoNoOrg` (or re-resolve to a remaining membership) instead of
  returning `ErrOrganizationNotFound` → 401. A stale tab reloading after the org
  is deleted should degrade to an org-less session, not a logout.

### 2. dash0: switch or land on `/no-org`, never `logout()`

In `handleDeleteOrg` (`organization.settings.tsx:159-173`):

- Drop the unconditional `logout()` + `navigate("/login")`.
- Adopt the new session returned by the DELETE response (same client path as
  `switchOrg`, `web/dash0/src/context/AuthContext.tsx:412-450`), then:
  - remaining orgs → `navigate({ to: "/orgs/$org", params: { org: next } })`,
  - none → `navigate({ to: "/no-org" })`.
- Scope cache invalidation: `useDeleteOrg`'s `onSuccess: queryClient.clear()`
  (`web/dash0/src/api/hooks.ts:2505-2517`) makes every mounted org query refetch
  against the dead org and feeds the expired-session race; clear/remove queries
  for the deleted org only, after navigation.
- `redirectToExpiredLogin()` (`api/client.ts:151-158`): when the current path's
  org is the one that just died, don't send the user to that slug's login page.
  Preferring the org-picker/`/no-org` over the hardcoded `"default"` fallback
  (also in `routes/login.tsx:29`, `routes/index.tsx:11`, `routes/no-org.tsx:85`)
  is in scope where it's cheap, but the primary fix is that the happy path never
  reaches `redirectToExpiredLogin` at all.

### 3. Tests

- **Backend**: table-driven handler tests for `DELETE /orgs/:org` asserting
  (a) caller with a second org gets 200 + a session scoped to the surviving org,
  (b) caller with no other org gets 200 + an org-less session whose `/auth/me`
  returns `organization: null`, (c) *other* members' refresh tokens are revoked
  (negative control: their `POST /auth/refresh` 401s), (d) `/auth/me` with a
  stale access token naming the deleted org returns 200 org-less, not 401.
- **E2E**: tighten `web/dash0/e2e/org-owner-delete.spec.ts:94-132` — after
  deletion assert the user is **still authenticated** and lands on the next org's
  dashboard (multi-org case) or `/no-org` (last-org case), with no
  `session_expired` banner; today it only asserts the URL left the deleted org,
  which passes for both the intended flow and the buggy logout.

## Implementation Plan

### Step 1 — backend: `DeleteOrg` mints a replacement session for the caller

`server/internal/handlers/auth/org_delete.go`

- Change the signature to
  `DeleteOrg(ctx, orgSlug, callerUserUID string, req DeleteOrgRequest, authContext Context) (*LoginResponse, error)`.
- Keep every teardown step exactly as-is (check jobs, checks, memberships,
  `DeleteUserTokensByOrg`, previous slugs, the org row) — revoking *other*
  members' sessions stays deliberate (spec 2026-08-08-11).
- After the delete, call a new `mintPostDeletionSession(ctx, callerUserUID, authContext)`:
  - `getOrganizationsForUser` re-resolves the caller's surviving memberships
    (the deleted org's membership is already soft-deleted, so it cannot come back).
  - ≥1 org left → `SwitchOrg(ctx, userUID, orgs[0].Slug, authContext)` (the
    existing switch machinery: fresh refresh-token row + org-scoped access token),
    then fill `Organizations` with the surviving list.
  - 0 orgs left → `completeLogin(ctx, user, nil, role, LoginActionNoOrg, orgs, …)`,
    i.e. the org-less access-token branch. No refresh token by design.
- The caller's *own* refresh token for the deleted org is revoked like everyone
  else's; the replacement session is a brand-new row, so the negative control in
  §3(c) stays honest.

### Step 2 — backend: `/auth/me` degrades instead of 401ing

`server/internal/handlers/auth/service.go` — in `GetUserInfo`, a
`GetOrganizationBySlug` miss (`sql.ErrNoRows`) now falls through to
`getUserInfoNoOrg` instead of returning `ErrOrganizationNotFound`. A stale tab
holding a token that names a vanished org reloads into an org-less session
rather than being logged out.

### Step 3 — backend: handler returns 200 + session

`handler.go` `DeleteOrg`: read the claims, pass `claims.UserUID` and the
`Context{UserAgent, RemoteAddr}`, then `setAccessTokenCookie` with the new token
(replacing `clearAuthCookie`) and `WriteJSON(200, resp)` instead of 204 —
mirroring the org-rename path.

### Step 4 — docs: OpenAPI + wiki

`server/internal/app/openapi/openapi.yaml` (`deleteOrg`: 204 → 200 with a new
`DeleteOrgResponse` schema) and `wiki/api-specification/orgs.md`.

### Step 5 — dash0: adopt the session, never `logout()`

- `api/hooks.ts` `useDeleteOrg`: type the response as `DeleteOrgResponse`, drop
  `queryClient.clear()` from `onSuccess` (the caller scopes the eviction after
  navigating).
- `contexts/AuthContext.tsx`: new `adoptOrgDeletionSession(session)` — overwrite
  both tokens; adopt the surviving org, or **clear** the stored org when the
  payload has none (so nothing keeps pointing at the dead slug).
- `api/client.ts`: `markOrgDeleted(slug)` tombstone; `redirectToExpiredLogin()`
  sends a racing request to `/no-org` instead of the dead org's login page.
- `routes/orgs/$org/organization.settings.tsx` `handleDeleteOrg`: mark the org
  deleted → adopt the session → navigate to the next org's dashboard or
  `/no-org` → evict only the deleted org's queries.
- Cheap hardcoded-`"default"` cleanups: `routes/index.tsx` (no org → `/no-org`)
  and `routes/no-org.tsx` (sign-out → `/login`, the single fallback owner).

### Step 6 — tests

- `server/internal/handlers/auth/org_delete_test.go`: table-driven HTTP-level
  tests for `DELETE /orgs/:org` covering §3 (a)/(b)/(c)/(d), plus the existing
  service-level tests updated for the new signature.
- `web/dash0/e2e/org-owner-delete.spec.ts`: assert still-authenticated + landing
  target for both the multi-org and last-org cases, and no `session_expired`.
