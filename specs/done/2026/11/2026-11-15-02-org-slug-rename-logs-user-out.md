---
model: opus
effort: high
---

# Renaming the organization slug can log the owner out (redirect to login?session_expired=true)

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/81

## Source

In production, a freshly registered owner opened Organization > Settings, uploaded a logo,
changed the organization slug and clicked **Save** in the profile card. The app navigated to
`/orgs/<new-slug>/organization/settings`, then about 80 ms later hard-navigated to
`/orgs/<new-slug>/login?session_expired=true&returnTo=…/organization/settings`. After signing in
again everything worked on the new slug. No console error and no error-tracking event in that
window. Expected: the rename keeps the user signed in. The report's three hypotheses are checked
below; the first and third are plausible, the second is not.

## Problem

What the code does on a rename:

- `UpdateOrgProfile` (`server/internal/handlers/auth/org_profile.go:124`) re-mints a session
  for the renamed org (`attachRenameSession`, `org_profile.go:270`, `mintOrgSession`,
  `org_profile.go:341`). It does not revoke the caller's previous session: the old access token
  and old refresh token stay valid. `startSession` evicts the least-recently-active sessions
  beyond `maxActiveSessions` = 10 (`handlers/auth/service.go:95`, `:1002`), not the old one in
  particular. So hypothesis 2 (no `accessToken` in the save response) is unlikely: the response
  carries a session whenever the slug changed and the caller is not impersonating.
- The card adopts that session then navigates (`web/dash0/src/components/shared/org-profile-card.tsx:183-197`,
  `adoptRenamedOrgSession` in `web/dash0/src/contexts/AuthContext.tsx:771`).
- A request still carrying the **old** token on the **new** slug gets a 403 from the token-scope
  gate (`server/internal/handlers/auth/orgaccess.go:63`), not a 401. A request to the **old**
  slug is redirected by `OrgSlugRedirect` (`server/internal/middleware/orgslug.go`) before auth.
  Neither path reaches `redirectToExpiredLogin` directly.
- `session_expired=true` is produced only by `handleResponse` on a 401
  (`web/dash0/src/api/client.ts:514-521`, which calls `clearToken()` then redirects, and does
  not log) and by `escalate` in `web/dash0/src/lib/token-refresh.ts:66-70` (which logs a
  `console.error`). The report saw no console error, which points at the silent `handleResponse`
  path: a 401 whose silent refresh-and-retry (`client.ts:458-484`) either retried and got
  another 401, or never retried.
- A real race exists in the refresh path: `doRefresh` reads the refresh token, POSTs, then
  `setSession(data.accessToken, …)` (`token-refresh.ts:126`). A refresh that started with the
  **old** refresh token before the save and resolves after `adoptRenamedOrgSession` overwrites
  the new access token with one scoped to the old slug, leaving the new refresh token in place:
  a mixed session. Likewise a reactive 401 retry in `fetchWithAuth` keeps the URL of the page
  being left.

Where the 401 itself comes from is **not** established by reading the code: it needs a
reproduction (see Proposal step 1). The report is right that the guard in the card does not
cover every in-flight request.

## Why a spec

- Correctness hinges on authorization scope and on concurrency between in-flight requests, the
  token refresh single-flight and the session swap.
- The root cause is not reproduced yet; the fix spans the auth client (`api/client.ts`,
  `lib/token-refresh.ts`, `AuthContext.tsx`), possibly the server's rename session handling,
  and needs a new E2E test: beyond a direct fix.

## Proposal

1. **Reproduce first** with a Playwright test in `web/dash0/e2e/org-profile.spec.ts` (an `acme`
   org): upload a logo, change the slug, save, while (a) the settings page's queries are still in
   flight (slow them with `page.route`), (b) a proactive refresh is in flight (force it by
   shortening the token expiry or calling the refresh during the save), (c) the logo upload just
   finished. Record which request 401s and why (backend log by `request_id`). The test asserts
   the user lands on `/orgs/<new-slug>/organization/settings` signed in, with no
   `session_expired` navigation.
2. **Fix the client races** found, at minimum:
   - Session generation: `adoptRenamedOrgSession` bumps a session epoch; `doRefresh` drops its
     result (does not call `setSession`) when the refresh token it spent is no longer the stored
     one, and `handleResponse` does not `clearToken()`/redirect for a 401 or 403 on a request
     that was sent with a token that is no longer the stored one (re-issue it once with the
     current token, or drop it silently).
   - Requests addressed to the old slug after the swap: cancel the old org's queries
     (`queryClient.cancelQueries` / `removeQueries` scoped to the old slug) inside
     `adoptRenamedOrgSession` before navigating, like the org-deletion flow does with
     `markOrgDeleted` (`client.ts:253-263`).
3. **Server, if step 1 shows it**: if the 401 comes from the old session (for example the old
   access token rejected once the new session is started), fix that on the server instead and
   cover it in `org_profile` Go tests.
4. **Tests**: the E2E of step 1, plus Vitest units on the epoch guard in `token-refresh.ts` and
   `client.ts` (a stale refresh result does not overwrite a newer session; a stale-token 401
   does not clear the session).

## Closing the issue

This spec closes fclairamb/solidping-business#81. The implementing PR body **must** carry a
`Closes fclairamb/solidping-business#81` line. When the spec is archived to `specs/done/`, verify
the issue is closed and, if not, close it by hand:

    gh issue close 81 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
