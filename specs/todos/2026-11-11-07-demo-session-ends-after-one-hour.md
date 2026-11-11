---
model: sonnet
effort: medium
---

# A live-demo visitor is sent back to the login page after one hour

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/66

## Source

In-app report (v0.46.0, Android Chrome, mobile viewport), filed from the demo org's login page
reached with `?session_expired=true`: "the session time is way too short". The reporter was
using the shared live demo and was logged out while still using it.

The report is right: on the demo org, every session ends one hour after login, whatever the
activity. It is a deliberate cap, not a bug in the refresh flow.

## Problem

- `server/internal/jobs/jobtypes/job_startup_demo.go:75-77` sets
  `demoSessionMaxDurationSeconds = 3600`, written as the demo org's
  `auth.session_max_duration` parameter at boot (`markDemoOrg`, `:176-187`) and re-applied by
  the cleanup job (`job_demo_cleanup.go:211-213`). Prod has it (org-scoped parameter on the demo
  org, value 3600).
- `refreshTokenExpiry` (`server/internal/handlers/auth/service.go:1496-1509`) caps the refresh
  token at `loginTime + session_max_duration`, so after one hour the refresh answers 401, and
  dash0 (`web/dash0/src/lib/token-refresh.ts`, `escalate("rejected")`) clears the session and
  redirects to `login?session_expired=true`.
- Reproduced against prod: a demo login returns `expiresIn: 3600` and a token with `demo: true`;
  the refresh works within the hour (200). The access/refresh mechanics are fine; only the cap
  ends the session.
- The cap's comment says "Visitors then log in again with one click; the credential is on the
  page", but the visitor lands on a page that reads like an error (`session_expired`), loses
  where they were (unless `returnTo` brings them back), and on mobile it feels like a bug.

## Why a spec

- A product/UX decision the reporter did not settle: keep a hard cap on the published demo
  credential, lengthen it, or hide it from the visitor.
- Touches authentication behaviour (session lifetime of a shared, public principal).

## Proposal

Recommended (see open question): keep the server cap, make its end invisible to the visitor.

1. In dash0, when a refresh is `rejected` and the dying session was a demo session (the access
   token's `demo` claim; dash0 also knows it as `isDemo` in `contexts/AuthContext.tsx`), sign in again with the public demo
   credential (from `/api/v1/config`, `demo.email` / `demo.password`) instead of redirecting,
   then retry the request that triggered the refresh. Single attempt; if that login fails, fall
   back to today's redirect.
2. Keep `returnTo` semantics: the visitor stays on the page they were on.
3. Show a one-line toast ("Demo session renewed") so the visitor knows why any unsaved form
   state may have been dropped.
4. Tests: a Vitest test of `token-refresh.ts` (demo session rejected → re-login called, no
   redirect; non-demo session rejected → redirect as today; re-login failing → redirect), and
   one Playwright test with a demo session whose refresh is mocked to 401.
5. Docs: the demo section of `wiki/` (or `web/docs` if it documents the demo) says sessions
   renew silently every hour.

## Open questions

- **Silent re-login (recommended), or simply raise the cap?** Recommended: silent re-login. It
  keeps the one-hour bound on any token minted from the published credential, and the visitor
  never sees it. Alternative: raise `demoSessionMaxDurationSeconds` to e.g. 24 h (one-line
  change), cheaper but leaves long-lived tokens from a public credential and still logs the
  visitor out eventually.

## Closing the issue

This spec closes fclairamb/solidping-business#66. The implementing PR body **must** carry a
`Closes fclairamb/solidping-business#66` line. When the spec is archived to `specs/done/`,
verify the issue is closed and, if not, close it by hand:

    gh issue close 66 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
