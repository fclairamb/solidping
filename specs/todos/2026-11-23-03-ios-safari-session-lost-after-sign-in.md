---
model: opus
effort: high
pr: batch
automerge: false
---

# iOS Safari: users land back on the `default` org login page, signed out, shortly after signing in

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/107

## Source

A PostHog daily review found two production sessions (Mobile Safari on iOS, two different
orgs, 2026-11-19 and 2026-11-22) with the same shape: sign-in, an opaque `Script error.`
(no stack) 20 s to 2 min later, then a fresh page load of `/d/orgs/default/login?session_expired=false`
without `returnTo`, and the user signs in a second time. In one session the user had to
submit the form again, so the login page did not recognise an existing session. The
`Script error.` only ever shows on iOS (Firefox iOS once before, without a sign-out). The
issue's first finding (escalation Select warning) is fixed in commit `700fc1ed`; this spec
covers the second one. No customer detail is to be reproduced (use `acme`).

## Problem

Not reproduced (needs an iOS device or WebKit). What the code says:

- `web/dash0/src/routes/index.tsx:65-72` (`RootRedirect`) sends to
  `/orgs/<stored org || "default">/login?session_expired=false` only when there is **no
  access token and no refresh token** in `localStorage`. Landing on `default` also means
  the stored org (`SESSION_ORG_KEY`, `contexts/AuthContext.tsx:314`) is missing. So at that
  point the whole session in `localStorage` is gone, or the page runs in a storage
  partition that never had it.
- The org login page does redirect an authenticated visitor into their org
  (`routes/orgs/$org/login.tsx:287-312`), so a page that shows the form saw no session.
- The tokens are cleared only by `clearToken` (`api/client.ts:133-140`) and the org by
  `clearStoredOrg` (`AuthContext.tsx:322`); an explicit logout path also leads there, but
  no logout event is in the replays.
- Candidate causes to tell apart:
  (a) a different storage context: the marketing site's "Sign in" link or the Google OAuth
      return opening in another browsing context (in-app browser, new tab after OAuth,
      Safari "Hide IP"/ITP partitioning) whose `localStorage` is empty;
  (b) WebKit clearing or failing to read `localStorage` right after sign-in (ITP 7-day
      cap does not fit 2 minutes; a quota or private-mode exception in `setToken` would);
  (c) a code path clearing the session on an error that WebKit surfaces as the opaque
      `Script error.` (e.g. the refresh escalation in `lib/token-refresh.ts`).
- The `Script error.` itself carries no frame: WebKit masks errors from cross-origin
  scripts. It may be unrelated noise; `lib/analytics.ts:288` (`dropKnownExceptionNoise`)
  already filters known noise in `before_send`.

## Why a spec

- Correctness hinges on sessions/authentication (step 2 table: authorization scope).
- Not reproducible here: it needs a WebKit reproduction and a diagnosis before any fix, and
  the filtering of `Script error.` is a product decision.

## Proposal

1. **Reproduce** with Playwright's WebKit project (and iPhone device emulation) on a
   gitignored `e2e/debug-*.spec.ts`: sign in (password and, if possible, the Google button's
   return path), open My alerts, then (i) reload, (ii) come back via the marketing site's
   "Sign in" URL, (iii) navigate back/forward. Record `localStorage` keys after each step.
   If WebKit emulation does not reproduce, test on a real iOS device through the PostHog
   replay steps.
2. **Instrument** (cheap, whatever the outcome): when `RootRedirect` or the org login page
   finds no session, capture a PostHog event `session_missing_on_load` with which keys are
   present (`token`, `refresh`, `org`: booleans only, never values), the referrer origin and
   whether `window.opener`/standalone mode applies. Also capture a breadcrumb when
   `clearToken` runs, with the caller reason. This tells (a), (b) and (c) apart in
   production.
3. **Fix** according to the cause found: e.g. make the OAuth return land in the same
   context, make `setToken` resilient to a `localStorage` exception (fallback to memory and
   a warning), or stop clearing the session on a non-auth error.
4. **Noise**: if `Script error.` with no stack proves unrelated, add it to
   `KNOWN_EXCEPTION_NOISE` in `lib/analytics.ts` (only when the event has no stack frames).

## Tests

- Vitest for the instrumentation helper (keys reported as booleans, no token value ever in
  the event) and, if added, the `Script error.` noise filter (dropped only without frames;
  a `Script error.` with a stack, or any other message, passes).
- A Playwright WebKit test of the fixed scenario: sign in, reload / come back from the
  marketing URL, still signed in and redirected into the org.

## Open questions

1. **Ship the instrumentation first and fix later, if WebKit emulation does not reproduce?**
   Recommended: yes, ship step 2 alone in that case, then reopen the diagnosis with the
   production events; trade-off: the bug stays live one more release.
2. **Drop stackless `Script error.` events in `before_send`?** Recommended: yes, but only
   once step 1/2 shows they do not precede the sign-out; dropping them earlier would hide
   the only signal of cause (c).

## Closing the issue

This spec closes fclairamb/solidping-business#107 (its first finding is already fixed by
`700fc1ed`). The implementing PR body **must** carry `Closes fclairamb/solidping-business#107`.
When the spec is archived to `specs/done/`, verify the issue is closed and, if the merge did
not close it, close it by hand:

    gh issue close 107 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
