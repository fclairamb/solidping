# Zero-org session gets logged out almost immediately

## Problem

A user who logs in with **zero organization memberships** lands on `/no-org`
([no-org.tsx](web/dash0/src/routes/no-org.tsx)), which offers "create an
organization" / "join an organization" cards. In practice this session dies on
essentially the next page load or reload, before the user gets to use either
card — discovered while QA-ing
[specs/todos/2026-07-08-09-create-org-missing-org-scoped-access-token.md](specs/todos/2026-07-08-09-create-org-missing-org-scoped-access-token.md)
by driving a *real* zero-org user through the *real* `/no-org` page in a
browser (not the existing e2e test's stubbed-`/auth/me` shortcut). This is
unrelated to that spec's fix (which is correct and already covered by passing
tests) — it's a separate, pre-existing, higher-severity gap in the same
zero-org surface.

### Root cause

`completeLogin`'s zero-org branch
([server/internal/handlers/auth/service.go:569](server/internal/handlers/auth/service.go#L569))
mints an access token with an **empty `orgSlug` claim** and **no refresh
token at all** — by design, since there's no org to scope a refresh token to:

```go
if resolvedOrg == nil {
    accessToken, tokenErr := s.generateAccessToken(user.UID, "", role, "")
    ...
    return &LoginResponse{AccessToken: accessToken, ...}, nil
}
```

Two independent mechanisms then break for `claims.OrgSlug == ""`:

1. **`Service.GetUserInfo`** (`GET /api/v1/auth/me`, backs `Me` in
   [server/internal/handlers/auth/handler.go:218](server/internal/handlers/auth/handler.go#L218))
   unconditionally resolves the org first:
   ```go
   // server/internal/handlers/auth/service.go:1333
   org, err := s.db.GetOrganizationBySlug(ctx, claims.OrgSlug)
   ```
   For `claims.OrgSlug == ""` this is always `sql.ErrNoRows` →
   `ErrOrganizationNotFound`, which `handleUserInfoError`
   ([server/internal/handlers/auth/handler.go:436](server/internal/handlers/auth/handler.go#L436))
   maps to **401 `ORGANIZATION_NOT_FOUND`** — reproduced live with
   `curl -H "Authorization: Bearer <zero-org-token>" /api/v1/auth/me`.

2. **`web/dash0/src/contexts/AuthContext.tsx`'s `validateSession()`**
   (runs on every app mount, [AuthContext.tsx:138](web/dash0/src/contexts/AuthContext.tsx#L138)):
   - If the token has no `expires_at`/`expires_in` metadata in
     `localStorage` (true for a fresh zero-org login, since it's treated as a
     "legacy/partial session"), it calls `refreshWithOutcome()`
     ([AuthContext.tsx:154](web/dash0/src/contexts/AuthContext.tsx#L154)) up
     front. A zero-org session has no refresh token, so this fails, and
     `escalate("no-refresh-token")` in
     [web/dash0/src/lib/token-refresh.ts:61](web/dash0/src/lib/token-refresh.ts#L61)
     clears the session and hard-redirects to
     `login?session_expired=true`.
   - Even when `expires_at` *is* present (so the branch above is skipped),
     `validateSession()` still calls `/api/v1/auth/me` with
     `suppress401Redirect: true`
     ([AuthContext.tsx:164](web/dash0/src/contexts/AuthContext.tsx#L164)), and
     its `catch` block treats the resulting 401 as an auth failure and clears
     the token locally (no redirect, but the session is gone all the same,
     [AuthContext.tsx:186](web/dash0/src/contexts/AuthContext.tsx#L186)).

   So bug #1 alone is sufficient to silently destroy a legitimate zero-org
   session on the very next page load, independent of whether bug #2's
   specific "no refresh token" path fires.

Net effect: a real zero-org user is bounced back to
`login?session_expired=true` on essentially any full page reload — before
they ever reach the create-org or join-org cards on `/no-org`. This is likely
why [web/dash0/e2e/membership-requests.spec.ts](web/dash0/e2e/membership-requests.spec.ts)'s
"`/no-org` screen exposes both create and join cards" test stubs
`GET /api/v1/auth/me` entirely rather than driving a real zero-org login —
that test may have been written around this exact limitation instead of
exercising the real backend.

### `GET /api/v1/features` — checked, looks unaffected

The description flagged a 401 observed on `/api/v1/features` for a zero-org
token and asked to audit it for the same pattern. On inspection
([server/internal/handlers/features/handler.go](server/internal/handlers/features/handler.go)),
`GetFeatures` does no org resolution at all — it's pure config
(`h.cfg.App.EnableBugReport`) — and the route is registered with only
`authMiddleware.RequireAuth`
([server/internal/app/server.go:904](server/internal/app/server.go#L904)),
which does not require a non-empty `OrgSlug` claim. So this endpoint should
**not** reproduce the bug on its own; the previously observed 401 was likely
a downstream symptom of the session already having been cleared by bug #1/#2,
not an independent bug in `/features`. Worth a quick live re-check once the
`/auth/me` fix lands, but no separate fix is expected here.

## Proposal

- **`Service.GetUserInfo`** ([server/internal/handlers/auth/service.go:1332](server/internal/handlers/auth/service.go#L1332)):
  treat `claims.OrgSlug == ""` as a valid "no org" state instead of an error.
  Skip the `GetOrganizationBySlug` / membership-role lookup in that case,
  return `Organization: nil`, `Organizations` from `s.getOrganizationsForUser`
  (as today), and a role that makes sense with no org (e.g. empty string or a
  dedicated "no-org" sentinel — check how the frontend consumes `role` before
  choosing). This mirrors how `completeLogin`'s zero-org branch already
  treats this as a legitimate outcome (`LoginActionNoOrg`), not a failure.
- **`MeResponse.Organization`** (wherever declared alongside `GetUserInfo`,
  service.go): make it a pointer / optional so a nil org is representable
  without inventing a fake organization.
- **Frontend `MeResponse` type**
  ([web/dash0/src/contexts/AuthContext.tsx:103](web/dash0/src/contexts/AuthContext.tsx#L103)):
  change `organization` from required to optional (`organization?:`), matching
  the sibling `AuthResponse.organization` field one block above
  ([AuthContext.tsx:89](web/dash0/src/contexts/AuthContext.tsx#L89)) which is
  already optional. `validateSession()`
  ([AuthContext.tsx:138](web/dash0/src/contexts/AuthContext.tsx#L138)) already
  guards `data.organization?.slug` before storing it, so once `/auth/me`
  stops erroring for zero-org tokens this should flow through with minimal
  changes — the main risk is the up-front `refreshWithOutcome()` call for
  tokens missing `expires_at`, which should be checked separately since a
  zero-org token never gets a refresh token by design (it may need to accept
  "no refresh token, but a valid me-fetch" as a non-failure outcome, not just
  auto-escalate to logout).
- **Test coverage**: add a real (non-stubbed) e2e test or Go integration test
  that logs a zero-org user in via the real flow, then calls
  `GET /api/v1/auth/me` (or reloads `/no-org`) and asserts the session
  survives — the kind of regression the existing stubbed
  `membership-requests.spec.ts` test would hide forever. Consider updating
  that existing test to exercise the real `/auth/me` response once fixed,
  rather than stubbing around the bug.

## Open questions

- What should `role` be in `MeResponse.User` when there's no org and no
  membership to derive a role from? Check how the dashboard uses `role` on
  `/no-org` before picking a value.
- Should the frontend's "no `expires_at` → force refresh" heuristic in
  `validateSession()` special-case the no-refresh-token zero-org case
  directly (skip the forced refresh when `/auth/me` alone can validate the
  session), or is a more general "missing refresh token is not automatically
  a failure" change safer? Needs re-reading `token-refresh.ts` in full before
  deciding.

## Implementation Plan

### Backend (`server/internal/handlers/auth/`)
1. **`Service.GetUserInfo`** (service.go): split out a `getUserInfoNoOrg` helper.
   When `claims.OrgSlug == ""`, take the no-org path: skip
   `GetOrganizationBySlug` + `GetMemberByUserAndOrg`, return
   `Organization: nil`, `Organizations` from `getOrganizationsForUser` (as
   today), and `role = ""` for a plain user or `RoleSuperAdmin` for a
   superadmin. The **non-empty-OrgSlug path is left byte-for-byte unchanged**
   (org lookup still first, same error precedence).
   - **`role` choice**: empty string for a plain no-org user. This mirrors the
     login response's no-org branch (`resolveFromMemberships` returns `role: ""`
     for `LoginActionNoOrg`) and matches what the dashboard already consumes on
     `/no-org` (create-org.spec.ts stubs `/auth/me` with `user: { role: "" }`).
     `isAdmin`/`isSuperAdmin` both derive false from it — correct with no org.
2. **`MeResponse.Organization`** is already `*OrganizationInfo`; add
   `omitempty` so a nil org serialises as an absent field (matching the sibling
   `LoginResponse.Organization` and the frontend's optional type) instead of
   `null`.

### Frontend (`web/dash0/src/contexts/AuthContext.tsx`)
3. Change `MeResponse.organization` from required to optional (`organization?:`),
   matching `AuthResponse.organization`.
4. Fix `validateSession()`: gate the up-front `refreshWithOutcome()` (the
   `getExpiresAt() === null` branch) on there actually being a refresh token
   (`getRefreshToken() !== null`). A zero-org session has no refresh token by
   design, so with no refresh token we skip the forced refresh and let
   `/auth/me` be the arbiter: a 200 (org absent) keeps the session, a 401/403
   still clears it in the `catch` block. Legacy sessions *with* a refresh token
   keep the exact same up-front-refresh behavior (zombie-loop protection
   intact). The `catch`/store logic already guards `data.organization?.slug`, so
   a nil org flows through untouched.

### Tests
5. Go integration test: seed a zero-org user, log in for real, call
   `GET /api/v1/auth/me`, assert 200 with nil Organization and empty role (not
   401).
6. Playwright e2e: real zero-org login + reload of `/no-org` asserting the
   session survives (no redirect to `login?session_expired=true`), hitting the
   real (un-stubbed) `/auth/me`.
