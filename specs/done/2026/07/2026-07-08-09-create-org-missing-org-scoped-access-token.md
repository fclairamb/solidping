# Creating an org from the no-org page leaves the user holding a token scoped to no org — every subsequent API call 403s

## Problem

Found during the 2026-07-08 funnel audit of login-shaped frontend paths
([`2026-07-08-01-per-org-session-duration-and-longer-token-refresh.md`](../done/2026/07/2026-07-08-01-per-org-session-duration-and-longer-token-refresh.md)
scope was session-duration/token-refresh only; this is a separate,
pre-existing bug noticed along the way).

A user with **zero org memberships** logs in and lands on `/no-org`
([`web/dash0/src/routes/no-org.tsx`](../../web/dash0/src/routes/no-org.tsx)).
That login already happened through `completeLogin`
(`server/internal/handlers/auth/service.go:564-578`), which for
`resolvedOrg == nil` mints an access token via
`s.generateAccessToken(user.UID, "", role, "")` — **org slug claim is the
empty string**, and no refresh token / session row is created at all (this
part is intentional: there's no org to scope a session to yet).

From `/no-org`, `CreateOrgCard.handleSubmit` calls `useCreateOrg()`
(`web/dash0/src/api/hooks.ts:1857-1868`) → `POST /api/v1/orgs` →
`Handler.CreateOrg` (`server/internal/handlers/auth/handler.go:618-658`) →
`Service.CreateOrg` (`server/internal/handlers/auth/service.go:2521-2568`).
`CreateOrg` creates the organization, makes the caller an admin member, and
returns:

```go
return &OrgResponse{
    UID:  org.UID,
    Slug: org.Slug,
    Name: org.Name,
}, nil
```

No access token, no refresh token — `OrgResponse` doesn't even have those
fields. The frontend then does (`no-org.tsx`):

```ts
const result = await createOrg.mutateAsync({ name, slug });
navigate({ to: "/orgs/$org", params: { org: result.slug } });
```

navigating straight to `/orgs/<newOrgSlug>` while `localStorage` still holds
the original no-org token (org claim `""`).

Every org-scoped route (`/api/v1/orgs/:org/...`) is guarded by
`AuthMiddleware` (`server/internal/middleware/auth.go:172-176`):

```go
if !claims.IsSuperAdmin() {
    if claims.OrgSlug != orgSlug {
        // 403 "Access to this organization is denied"
    }
}
```

`claims.OrgSlug` is `""`, `orgSlug` is the new org's slug — they never
match for a non-super-admin user. So a regular user who creates their first
org lands on the org dashboard and **every single API call 403s**
("Access to this organization is denied"). Super admins are exempt
(`IsSuperAdmin()` short-circuits the check), so this is easy to miss while
testing as a super-admin account, and the bug is specific to regular users
creating their very first org.

This gap is already flagged in code comments left by the funnel audit —
`web/dash0/src/api/hooks.ts:1854-1856` on `useCreateOrg`:

> "Organization creation hook. The response never carries a fresh token
> (see `server/internal/handlers/auth/service.go` `OrgResponse`) — the
> caller's existing access token is used as-is, unlike the other
> login-shaped paths."

— but the underlying gap itself was left alone as out of scope for that spec.

### Reference: how `SwitchOrg` does it right

`Service.SwitchOrg` (`server/internal/handlers/auth/service.go:1657-1748`)
already solves the "mint a token scoped to a *different* org than the one
in hand" problem: it looks up the caller's role in the target org, creates
a fresh `models.TokenTypeRefresh` row scoped to the org
(`models.NewUserToken(user.UID, &org.UID, ...)`), calls
`s.enforceSessionCap(ctx, user.UID)`, then mints the access token bound to
that refresh token: `s.generateAccessToken(user.UID, org.Slug, role,
refreshToken.UID)`. `CreateOrg` should follow the same shape — the only
difference is the org and membership are brand new rather than pre-existing.

## Proposal

### Backend

1. In `Service.CreateOrg` (`server/internal/handlers/auth/service.go:2521`),
   after creating the organization and the admin membership, mint a session
   for the new org exactly like `SwitchOrg` does: generate a refresh token,
   persist it via `s.db.CreateUserToken` scoped to `org.UID`, call
   `s.enforceSessionCap(ctx, user.UID)`, then
   `s.generateAccessToken(user.UID, org.Slug, models.MemberRoleAdmin, refreshToken.UID)`.
2. Extend `OrgResponse` (or return a `LoginResponse`-shaped payload, matching
   what `SwitchOrg`'s handler returns) with `accessToken`, `refreshToken`,
   `expiresIn`, `tokenType` alongside the existing `uid`/`slug`/`name`.
   Check how the `SwitchOrg` handler shapes its HTTP response and mirror it
   rather than inventing a new shape.
3. Update `handler.go:CreateOrg` if the response type changes.

### Frontend

4. Update the `useCreateOrg` return type in `web/dash0/src/api/hooks.ts` to
   include the new token fields, and remove the now-stale comment that
   documents the gap as expected behavior.
5. In `no-org.tsx`'s `CreateOrgCard.handleSubmit`, call `setSession(...)`
   (from `web/dash0/src/api/client.ts`) with the returned
   `accessToken`/`refreshToken`/`expiresIn` before navigating — mirror how
   `AuthContext.tsx` calls `setSession` after login/switch-org
   (`web/dash0/src/contexts/AuthContext.tsx:232,295,317,344`).

### Verify super-admin path is unaffected

A super-admin creating an org bypasses the claims check entirely
(`middleware/auth.go:172`), so they'd never have hit this 403 — confirm the
fix doesn't change behavior for that path (it shouldn't: minting a correctly
scoped token for a super-admin is a no-op improvement, not a behavior
change, since `IsSuperAdmin()` already lets any org slug through).

## Out of scope

- The join-org / membership-request flow (`JoinOrgCard`) — that path doesn't
  navigate into an org on submit, it only shows a pending-request state, so
  it isn't affected by this bug.
- Any other funnel-audit findings from the session-duration spec; this is
  filed as a standalone bug because it's unrelated to session duration or
  token refresh.

## Acceptance criteria

- Backend test: a user with zero org memberships logs in (zero-org
  `completeLogin` branch), calls `CreateOrg`, and the returned response
  contains an `accessToken` whose JWT `orgSlug` claim equals the new org's
  slug — verified by decoding the token and/or by calling an org-scoped
  endpoint with it and getting a 2xx instead of 403.
- Backend test: the *original* no-org token still 403s on the new org (it
  should — creating an org doesn't retroactively fix the old token; the
  frontend must adopt the new one).
- Backend test: `SwitchOrg` behavior and response shape are unchanged by
  this refactor if any shared helper is extracted.
- Frontend test (Playwright, `web/dash0/e2e/`): a fresh zero-org user
  creates an org from `/no-org` and lands on a working `/orgs/$org`
  dashboard with no 403s (add to existing no-org / onboarding e2e coverage
  if present, otherwise a new spec file).
- `make build-backend lint-back test` green.
- `make build-dash0 && cd web/dash0 && bun run lint` green.

## Implementation plan

- [x] Backend: mint org-scoped refresh + access token in `CreateOrg`,
      mirroring `SwitchOrg`; extend `OrgResponse` (or switch to the
      `SwitchOrg`/`LoginResponse` shape) with the token fields.
- [x] Backend tests: zero-org token 403s pre-fix repro test, then green
      post-fix; new-org token accepted; old token still rejected;
      `SwitchOrg` regression check.
- [x] Frontend: update `useCreateOrg`'s return type, call `setSession(...)`
      in `no-org.tsx` before navigating, drop the stale gap comment.
- [x] Frontend/e2e: cover the create-org-as-zero-org-user journey end to
      end.
- [x] Verify: `make build-backend lint-back test`,
      `make build-dash0 && cd web/dash0 && bun run lint`.

### Findings during implementation

- Chose to extend `OrgResponse` (flat `uid`/`slug`/`name` +
  `accessToken`/`refreshToken`/`expiresIn`/`tokenType`) rather than
  switching to the nested `LoginResponse` shape, since the existing
  frontend consumer already expects the flat shape and the spec explicitly
  allowed either option.
- `CreateOrg`'s handler now also sets the `access_token` cookie, matching
  every other login-shaped response (Login, SwitchOrg, Verify2FA, …).
- While manually verifying the e2e flow against a real browser + real
  backend, found two separate, pre-existing bugs unrelated to this fix
  (both flagged as follow-up tasks, not fixed here):
  1. `auth.Service` holds a stale, boot-time-frozen copy of `AuthConfig`,
     so enabling registration via the `auth.registration_email_pattern`
     system parameter (env or DB) never actually reaches
     `Service.Register()`, even though `GET /api/v1/auth/providers`
     reports it as enabled. This is why the committed e2e test's register
     step gracefully skips in most environments (same precedent as the
     existing `membership-requests.spec.ts` "admin approve flow" test).
  2. A genuine zero-org session (exactly the state `/no-org` exists to
     handle) gets logged out almost immediately on a real page load/reload:
     `GetUserInfo` (`/api/v1/auth/me`) unconditionally resolves
     `claims.OrgSlug` via `GetOrganizationBySlug` and 401s for the empty
     zero-org slug, and the frontend's `validateSession()` treats a
     token without refresh-token/expiry metadata as a "legacy session"
     needing an up-front refresh that fails (no refresh token by design)
     and hard-redirects to login. The committed e2e test works around this
     with the same `/auth/me` stubbing technique the existing "/no-org
     screen exposes both create and join cards" test already uses.
  Both were reproduced live (curl + Playwright) against a side-car
  `SP_RUNMODE=test` server, with the full create-org-from-/no-org flow
  passing end-to-end once these two were worked around — confirming the
  fix in this spec is correct and complete.
