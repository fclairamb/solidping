---
model: sonnet
effort: medium
---

# A first-time visitor must not be told their session expired

## Problem
Open `https://solidping.io/` in a fresh browser (no cookies, no localStorage). It lands on
`/d/orgs/default/login?session_expired=true&returnTo=%2Fd%2Fno-org`, and the sign-in card shows
"Your session has expired. Please log in again." (`auth.json` `sessionExpired`). The visitor never
had a session. Opening `/d/orgs/default/login` directly shows no banner.

The bare domain is what people type and what directory listings store. Every new visitor arriving
that way is told something went wrong before they have done anything. PostHog shows real signups
starting from that URL (2026-10-11).

The path:
1. `RootRedirect` (`web/dash0/src/routes/index.tsx:60`): no resolved org → `/no-org`. An
   unauthenticated visitor has no org either, so they get the same redirect.
2. `/no-org` mounts and fires authenticated requests (`/auth/me`, `/auth/membership-requests`).
   They answer 401.
3. `apiFetch` tries a refresh. `doRefresh` (`web/dash0/src/lib/token-refresh.ts:~80-100`) already
   knows "no access token and no refresh token" means signed out and returns quietly (spec
   2026-09-25-14).
4. But the 401 then reaches `handleResponse` (`web/dash0/src/api/client.ts:478`), which always
   calls `clearToken()` and `redirectToExpiredLogin()` (`:289`). That one always writes
   `session_expired=true`.

Secondary: the card title reads "Sign in to default" (`signInTo`, `routes/orgs/$org/login.tsx:637`).
For a stranger, `default` means nothing.

## Proposal
1. `RootRedirect`: when the auth context has resolved and there is no session at all (no access
   token, no refresh token), go straight to the login page with `session_expired: false` instead
   of `/no-org`. Same stored-org-else-`default` fallback as the root `/login` route. This also
   removes the 401 round trip and the flash of `/no-org`.
2. `handleResponse` 401 branch: read whether a token existed **before** `clearToken()`. If none
   did, redirect to the login page with `session_expired=false` (keep `returnTo`). Only a
   rejected token that existed earns the banner. Do this in `redirectToExpiredLogin` itself if
   that is cleaner, e.g. a `{ hadSession }` argument, so every caller gets it.
3. Keep every existing expired-session behaviour: a real expired or revoked token still shows the
   banner and keeps `returnTo`.
4. Optional, same PR if small: on `/orgs/default/login` reached without an explicit org choice,
   show the generic "Sign in" title instead of "Sign in to default". Skip it if it needs more
   than a title change.

## Tests
- Playwright (`web/dash0/e2e/`, next to `zero-org-session.spec.ts` / `login.spec.ts`): a fresh
  context opening `/d/` lands on a login URL with no `session_expired=true`, and
  `login-title` is visible with no expired banner.
- Playwright: a signed-in session whose tokens are then invalidated server-side still lands on
  the login page **with** the banner (regression control, compare `session-continuity.spec.ts`).
- Unit (`web/dash0/src/api/redirect-to-expired-login.test.ts`): no stored token → URL carries
  `session_expired=false`. Stored token → `session_expired=true` as today.

## Acceptance Criteria
- [ ] A fresh browser opening `/` or `/d/` never sees "Your session has expired".
- [ ] An actually expired or revoked session still sees it.
- [ ] `returnTo` keeps working in both cases.
