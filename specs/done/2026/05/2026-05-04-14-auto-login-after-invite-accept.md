# Auto-login after invitation acceptance (no redirect to login page)

## Context

When a user accepts an organization invitation, the expected flow is:

1. Click the email link → `/dash0/invite/{token}`
2. (New user) fill in name + password, or (authenticated user) click "Accept"
3. Land on the org dashboard, already logged in

Step 3 is broken. Users currently get bounced to the login page (`/orgs/$org/login?returnTo=...`) and have to enter their credentials a second time, even though the password they just set during acceptance is the right one and the backend has already issued a session.

The bug looks invisible at first glance because the invite page *does* call `setToken()`. Reading the code more carefully:

- `web/dash0/src/routes/invite.$token.tsx:41` (authed branch) and `:76` (new-user branch) call `setToken(result.accessToken)` — this only writes to `localStorage`.
- The page then does `navigate({ to: "/orgs/$org" })`.
- `web/dash0/src/routes/orgs/$org.tsx:56` runs `beforeLoad` synchronously and checks `context.auth?.isAuthenticated`.
- `isAuthenticated` is defined at `web/dash0/src/contexts/AuthContext.tsx:278` as `!!user`, where `user` is React state set by `validateSession()` / `login()` / `loginWithOAuth()`.
- The invite page never updates `user`, `org`, or `organizations` in `AuthContext`. So `isAuthenticated` is `false`, `isLoading` already finished long ago, and the guard throws a redirect to the login page.

By contrast, `login()` (`AuthContext.tsx:146`) and `loginWithOAuth()` (`:192`) both call `setToken()` *and* `setUser()` / `setOrg()` / `setOrganizations()` — that's why they don't trip the guard.

A second-order issue: the backend `Service.AcceptInvite` (`server/internal/handlers/auth/service.go:2478-2494`) populates `User` and `Organization` (singular) on the `LoginResponse`, but never `Organizations` (plural). Login does — see `LoginResponse` at `service.go:197-208`. The frontend then has no list of orgs after accepting an invite, even if it did wire the response into `AuthContext`.

## Scope

**In scope:**
- After `POST /api/v1/auth/accept-invite` succeeds, the user lands on `/orgs/{org}` fully authenticated. No login page in between, no credential re-entry.
- Both branches: authenticated user accepting a new-org invite, and brand-new user creating their account via the invite.
- Backend response includes the user's full org list, so the sidebar org switcher works immediately.

**Out of scope:**
- Changing the password requirement for new users (they still set their password during acceptance — that's not "re-entering", that's the initial set).
- 2FA on invite acceptance (the `Requires2FA` / `TempToken` path is not currently exercised by accept-invite; leave it alone).
- The cookie set by `Handler.AcceptInvite` (`handler.go:759-766`) — orthogonal, no change needed.

## Approach

### 1. Frontend — populate `AuthContext` from the invite response

`web/dash0/src/contexts/AuthContext.tsx` — add an `acceptInviteSession(response)` method (name TBD) that mirrors what `login()` does, taking the `LoginResponse` shape directly:

```tsx
const acceptInviteSession = (data: AuthResponse) => {
  setToken(data.accessToken);
  if (data.organization?.slug) {
    setStoredOrg(data.organization.slug);
    setOrg(data.organization.slug);
  }
  setUser({
    email: data.user.email,
    name: data.user.name,
    avatarUrl: data.user.avatarUrl,
    roles: [data.user.role],
    isAdmin: data.user.role === "admin" || data.user.role === "superadmin",
    isSuperAdmin: data.user.role === "superadmin",
  });
  setOrganizations(data.organizations || []);
};
```

Expose it through the context value alongside `login`, `loginWithOAuth`, etc.

`web/dash0/src/routes/invite.$token.tsx` — replace the two raw `setToken(result.accessToken)` calls (lines 41 and 76) with `acceptInviteSession(result)`. Keep the `navigate(...)` afterward.

Update the response type returned by `useAcceptInvite` (`web/dash0/src/api/hooks.ts:1431-1449`) to include `organizations` so the new context method compiles cleanly.

### 2. Backend — return the user's full org list from accept-invite

`server/internal/handlers/auth/service.go:2478-2494` — populate `Organizations` on the returned `LoginResponse`. The simplest path: after the membership is created/confirmed, reuse whatever helper `Service.Login` uses to build `[]OrganizationSummary` for the user. Grep for `[]OrganizationSummary{` or the helper that backs `loginAction` resolution; reuse, don't reimplement.

If that helper doesn't exist as a standalone function yet, extract it.

### 3. Honest opinion / decision point worth flagging

While reading this flow I noticed: in the new-user branch the form collects an `email` field that the backend silently ignores — `AcceptInviteRequest` has no `email` field (`service.go:2189-2193`); the email is taken from the invitation entry. That's confusing UX (the user could type a different email than the invitation and nothing rejects it) but it's a separate cleanup. Mention it here so it's tracked, but don't fold it into this spec.

Also: for an *existing* user who happens to be logged out and clicks an invite for a new org, the form prompts for a password and the backend doesn't validate it (`service.go:2402-2421` only hashes the password when creating a new user; for an existing user the password is dropped on the floor). That's a real UX/security smell, but again — not this spec. File separately.

## Verification

1. Manual: log out completely, accept an invitation as a brand-new user. Land directly on `/orgs/{org}` with the sidebar populated. No login page in between.
2. Manual: while logged in to org A, accept an invite to org B. Land on `/orgs/{B}` — no login page, sidebar shows both orgs in the org switcher.
3. Refresh the dashboard after auto-login: still authenticated (token persisted, `validateSession()` succeeds).
4. `make test` — extend an existing accept-invite handler test (if any) to assert `Organizations` is non-empty in the response. If no such test exists, add one in `server/internal/handlers/auth/service_test.go`.
5. Playwright smoke (optional, only if there is already an invite-flow E2E to extend): no redirect to `/login` after acceptance.

## Implementation Plan

1. **Backend** — populate `Organizations` on the `LoginResponse` returned by `Service.AcceptInvite`. Reuse the existing org-list helper used by `Service.Login`.
2. **Frontend hook types** — extend the response shape in `useAcceptInvite` (`web/dash0/src/api/hooks.ts`) to include `organizations`.
3. **AuthContext** — add `acceptInviteSession(response)` that calls `setToken` + `setUser` + `setOrg` + `setOrganizations`, modeled on `login()`. Export it from the context value.
4. **Invite page** — in `web/dash0/src/routes/invite.$token.tsx`, replace both `setToken(result.accessToken)` calls with `acceptInviteSession(result)`.
5. **Backend test** — assert `Organizations` is populated on `AcceptInvite` success.
6. **QA** — `make build-backend lint-back test` then `make dev-test`, run through the manual cases in Verification.
