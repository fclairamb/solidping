---
model: sonnet
effort: medium
---

# Every logout logs 3 to 4 "token refresh failed: no-refresh-token" errors

## Problem

Three of the four sessions with errors on 2026-09-25 (05:59 to 06:00 UTC)
show the same sequence on sign-out from the sidebar:

```
POST /api/v1/auth/logout                          200
console.error  [auth] token refresh failed: no-refresh-token   ×3-4 (within 3 ms)
GET  /api/v1/orgs/<slug>/<something>              401
```

`logout()` in [AuthContext.tsx](web/dash0/src/contexts/AuthContext.tsx) awaits
`POST /auth/logout` (which revokes the session server-side), then clears the
tokens in `finally`. The dashboard's React Query queries are still mounted and
refetching on their intervals. Each request made with the now-revoked token
gets a 401. `apiFetch` then calls `refreshAccessToken()`, which finds no
refresh token and `escalate("no-refresh-token")`s: `console.error`,
`clearToken()`, `redirectToExpiredLogin()`. The redirect is a no-op because we
are already on `/login`, but the error is logged each time.

This is a real error only from the logs' point of view. For the user it's
invisible. But once spec `2026-09-25-13` turns on `capture_console_errors`,
every logout will file 3 to 4 exceptions in PostHog, and a real
`no-refresh-token` (a session that thinks it's authenticated but has no
refresh token) will be buried under them.

## Proposal

1. `logout()` stops server-bound work **before** revoking:
   `queryClient.cancelQueries()` then `queryClient.clear()`, and close the live
   socket (`live-socket.ts`) so it does not redial with a dead token. Then call
   `POST /auth/logout`, then clear local state as today. AuthContext has no
   access to the QueryClient today; pass it in or use `useQueryClient()`,
   whichever matches existing code.
2. In `doRefresh` ([token-refresh.ts](web/dash0/src/lib/token-refresh.ts)):
   when there is **no access token either**, the browser is simply signed out.
   Return `{ accessToken: null, failureReason: "no-refresh-token" }` without
   logging and without `escalate()`. Keep the `console.error` + escalate for
   the case it was written for: an access token present with no refresh token.
3. A module-level `loggingOut` flag (set at the start of `logout()`, cleared
   when it settles) suppresses the reactive 401 → refresh path in `apiFetch`
   for requests that were already in flight.

## Tests

- Unit (`token-refresh.test.ts`): no access token and no refresh token → no
  `console.error`, no redirect; access token without refresh token → still
  `console.error` + escalate (positive control).
- Playwright: sign in, open the dashboard (which has refetching queries), sign
  out from the sidebar. Collect `page.on("console")` messages of type `error`
  from the click until the login page has been idle for 3 s. Expect zero
  matching `token refresh failed`.
- Same test run with the sidebar sign-out on mobile width (420 px), which is
  how the prod sessions did it.
