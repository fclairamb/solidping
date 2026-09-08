---
model: opus
effort: high
---

# Entering the live demo from another org's login page strands the visitor in that org, and any session that lands on an org it cannot use gets a dead-end 403 instead of its own org

## Problem

### 1. The observed bug

On production (v0.26.0, which already carries #344 / spec
[2026-09-07-02](../done/2026/09/2026-09-07-02-demo-deep-link-and-own-check-edit.md)),
entering the live demo from `https://solidping.io/dash0/orgs/default/login` lands the
visitor in `/orgs/default`, not in the demo org. The demo user is not a member of
`default`, so every org-scoped request answers 403, the layout renders "Permission
Denied", and that card's only button links back to the very same org
([error-views.tsx:33](../../web/dash0/src/components/shared/error-views.tsx:33)) — a
dead end with no way into the demo.

Two paths produce it. One is a race, the other is deterministic:

**(a) The "Try the live demo" button.** `enterDemo`
([login.tsx:452](../../web/dash0/src/routes/orgs/$org/login.tsx:452)) calls
`login("demo", …)` and then `routeResult(result, "demo")`
([login.tsx:396](../../web/dash0/src/routes/orgs/$org/login.tsx:396)), which navigates to
`/orgs/demo`. But `login()` also flips `isAuthenticated`, and the "already authenticated"
effect at [login.tsx:382-387](../../web/dash0/src/routes/orgs/$org/login.tsx:382) then
fires `goToDestination(resolveDestination(org, …), replace = true)` with `org` being the
**URL's** org — `default`. `demoAutoLoginOwnsRedirect`
([lib/demo.ts:175](../../web/dash0/src/lib/demo.ts:175)) silences that effect only when
the `?demo` flag is present: spec 2026-09-07-02 §2 fixed exactly this race for the deep
link and left the button path with it. Whichever navigation commits last wins, and a
`replace` to `/orgs/default` overrides the in-flight `/orgs/demo`.

**(b) A returning visitor who still holds a demo session** (the demo session refreshes
like any other) opening `/orgs/default/login` *without* the flag — the marketing site's
plain login link, a bookmark, the browser's URL completion. No login happens at all; the
same effect sends them straight to `/orgs/default`. This one is not timing-dependent.

The existing E2E test catches neither. "the login page offers a one-click entry into the
demo" ([demo-account.spec.ts:41-53](../../web/dash0/e2e/demo-account.spec.ts:41)) waits
for `/\/orgs\/[^/]+/` — a pattern the login page's own URL already matches — and then
asserts the demo banner, which `DemoBanner` renders off `user.isDemo` on every org page
([demo-banner.tsx:34](../../web/dash0/src/components/shared/demo-banner.tsx:34)),
including the wrong one. A visitor stranded on `/orgs/test` with a demo session passes
that test.

### 2. The general problem the demo merely exposes

Any session can land on an org URL it cannot use: a bookmark to an org you were removed
from, a link a colleague pasted from *their* org, a slug from an old email. The org layout
already handles the *member-of-another-org* case — `needsOrgSwitch`
([$org.tsx:1017-1024](../../web/dash0/src/routes/orgs/$org.tsx:1017)) re-mints the token
via `switchOrg` when the URL's org is in `auth.organizations`. When it is **not**, the
comment says "non-members fall through to the normal 403 handling"
([$org.tsx:1010-1011](../../web/dash0/src/routes/orgs/$org.tsx:1010)): every child query
403s, `QueryErrorView` shows `PermissionDenied`
([error-views.tsx:143](../../web/dash0/src/components/shared/error-views.tsx:143)), and
its "Return to dashboard" loops on the same org. Nothing ever sends the user to an org
they *can* use — even though the client already holds the full list: `/auth/me` returns
`organizations` ([service.go:418](../../server/internal/handlers/auth/service.go:418)) and
`AuthContext` exposes it as `auth.organizations`
([AuthContext.tsx:95](../../web/dash0/src/contexts/AuthContext.tsx:95)) next to the
session's own org `auth.org`
([AuthContext.tsx:94](../../web/dash0/src/contexts/AuthContext.tsx:94)).

### 3. Which org to fall back to — decided

The ask left the choice open (last accessed / most checks / first created). Two facts
settle it cheaply:

- **The session's org *is* "last accessed".** Every login and every `switchOrg` re-mints
  the token for one org, so `auth.org` is by construction the org this browser used most
  recently. The backend already uses the same signal for the same purpose at login time:
  `resolveDefaultOrg`
  ([service.go:1051-1067](../../server/internal/handlers/auth/service.go:1051)) picks the
  org of the user's **most recent refresh token**, and only then falls back to the first
  membership ([service.go:1069-1077](../../server/internal/handlers/auth/service.go:1069)).
- **"First membership" already has one meaning; keep it.** `ListMembersByUser` orders
  `created_at DESC` ([sqlite.go:916](../../server/internal/db/sqlite/sqlite.go:916),
  [postgres.go:990](../../server/internal/db/postgres/postgres.go:990)), so `[0]` is the
  most recently *joined* org. That same `[0]` is what the login fallback returns
  ([service.go:957](../../server/internal/handlers/auth/service.go:957)), and the order
  is what the org switcher (`CommandMenu`) and the login `orgChoice` picker display.
  Re-ordering it for this feature would reshuffle three UIs for no user-visible gain.

So the rule is **session org, then `organizations[0]`** — the client-side mirror of what
the backend does on login. No new column, no checks count on `OrganizationSummary`, no
new endpoint.

## Proposal

### A. One pure helper: the org this session can use

Add `web/dash0/src/lib/accessible-org.ts` with a unit-tested pure function:

```ts
export function pickAccessibleOrg(
  urlOrg: string,
  session: { org: string | null; organizations: OrganizationSummary[]; isSuperAdmin: boolean },
): string | null
```

1. `isSuperAdmin` → `urlOrg` (super admins cross orgs on their claims alone,
   [$org.tsx:1010](../../web/dash0/src/routes/orgs/$org.tsx:1010)).
2. `urlOrg` is in `organizations` → `urlOrg`.
3. `session.org` is in `organizations` → `session.org` (last accessed, see §3).
4. `organizations[0]` if any.
5. otherwise `null` → the caller sends the user to `/no-org`
   ([routes/no-org.tsx](../../web/dash0/src/routes/no-org.tsx)).

Both call sites below go through it so the two redirects can never disagree.

### B. Login page: redirect an authenticated visitor to an org they can use

- The already-authenticated effect
  ([login.tsx:382-387](../../web/dash0/src/routes/orgs/$org/login.tsx:382)) resolves its
  destination with `pickAccessibleOrg(org, auth)` instead of the raw URL `org`, and lists
  `auth.org` / `auth.organizations` / `auth.user?.isSuperAdmin` in its dependencies so it
  evaluates against the *fresh* session after `applyLoginResponse`. With that, both
  branches of race (a) land in `demo`, and case (b) is fixed outright — a demo session on
  `/orgs/default/login` goes to `/orgs/demo`.
- Verify `applyLoginResponse` stores `organizations` from the **login** response (the
  `orgChoice` branch already reads `result.organizations`, so the payload carries them)
  and not only from a later `/auth/me`; otherwise step 3 of the helper sees an empty list
  right after login and the effect could still pick the URL org.
- `resolveDestination` ([login-destination.ts:43](../../web/dash0/src/lib/login-destination.ts:43))
  needs no change: it already refuses a `returnTo` whose org differs from the resolved
  org, so a stale `returnTo=/dash0/orgs/default/…` cannot drag the visitor back.
- The `?demo` flag path (`demoOwnsRedirect`) and the org picker (`showOrgPicker`) keep
  their current behaviour.

### C. Org layout: a non-member is sent to their own org, not to a 403

Next to `needsOrgSwitch` in [$org.tsx](../../web/dash0/src/routes/orgs/$org.tsx:1017),
add the complementary case: when `auth.isAuthenticated && !auth.isLoading && !isLoginPage`,
no OAuth token is in the URL (that flow does its own hard redirect,
[$org.tsx:1049-1071](../../web/dash0/src/routes/orgs/$org.tsx:1049)), and
`pickAccessibleOrg(org, auth) !== org`:

- navigate to `/orgs/$org` of the picked org with `replace: true` — the **org root**, not
  the current sub-path: check/status-page/incident uids do not carry across orgs, so a
  preserved sub-path would just 404 one hop later; `null` → `/no-org`.
- Guard against loops exactly like the switch effect does: at most one redirect per `org`
  (a ref like `switchingForOrgRef`), and never while `isSwitchOrgInFlight()`
  ([AuthContext.tsx:154](../../web/dash0/src/contexts/AuthContext.tsx:154)) — a switcher
  UI mid-`switchOrg()` transiently makes `auth.org` disagree with the URL.
- Super admins are untouched (helper step 1). A member who lacks a *role* for one page
  still gets `PermissionDenied` from `QueryErrorView` — that component stays as is; this
  spec removes only the non-member dead end.
- Show a short toast on this redirect ("You don't have access to `<slug>`, showing
  `<picked>` instead") using the existing toast primitive from the design reference. Add
  the key to **every** locale file — `bun run test:unit` is what catches a missing one.
  The demo cases in §B never reach this branch, so a first-time demo visitor never sees
  the toast.

Explicitly out of scope: org slug aliases after a rename (dash0 has no alias handling
today — a stale slug already 403s, and will now land you in your own org, which is
strictly better), and 404 handling for a super admin visiting an org that does not
exist.

### D. Tests

- **Unit** — `lib/accessible-org.test.ts`: table over super admin, URL org in list,
  session org preferred over `[0]`, `[0]` when the session org is gone, empty list → null.
- **E2E, demo** ([demo-account.spec.ts](../../web/dash0/e2e/demo-account.spec.ts)):
  - tighten the button test so it asserts the landing org is the demo org **and**
    `expect(page.url()).not.toContain("/orgs/test")`, the pattern the flag tests already
    use ([:108](../../web/dash0/e2e/demo-account.spec.ts:108),
    [:136](../../web/dash0/e2e/demo-account.spec.ts:136)). Run it several times before
    the fix to confirm it reproduces race (a) rather than passing by timing.
  - new: "a returning demo session opening /orgs/test/login (no flag) lands in the demo,
    not on Permission Denied" — case (b), deterministic.
- **E2E, general** (new file or `create-org.spec.ts`): sign in as the test user,
  `page.goto("orgs/not-my-org")` → URL ends on `/orgs/test`, the toast is visible, and
  no request answered 403 — copy the "no 403s" harness from
  [create-org.spec.ts:25](../../web/dash0/e2e/create-org.spec.ts:25). Add the super-admin
  variant if a fixture exists (URL org kept, no redirect).
- No backend change is expected. If the implementer does touch the membership ordering
  after all, the `resolveFromMemberships` tests and the org switcher order must move
  together — one ordering for all three consumers listed in §3.

## Implementation Plan

1. **§A — `web/dash0/src/lib/accessible-org.ts`.** Pure `pickAccessibleOrg(urlOrg, session)`
   implementing the five ordered rules (super admin → urlOrg; urlOrg in list; session org in
   list; `organizations[0]`; else `null`). Typed against `OrganizationSummary` from
   `AuthContext`. Table-driven vitest in `accessible-org.test.ts` covering every rule plus the
   "session org no longer a member" and "empty list" edges.

2. **§B — `web/dash0/src/routes/orgs/$org/login.tsx`.** The already-authenticated effect
   resolves through `pickAccessibleOrg(org, auth)`; `null` → `navigate({ to: "/no-org" })`.
   The effect's dependency array gains `auth.org`, `auth.organizations`,
   `auth.user?.isSuperAdmin` so it re-evaluates against the session `applyLoginResponse` just
   stored. `demoOwnsRedirect` and `showOrgPicker` branches are untouched.
   `applyLoginResponse` already stores `organizations` from the login response (falling back to
   `/auth/me` when the payload carries none) — verified, no change needed. `resolveDestination`
   unchanged: its org-match rule drops a stale cross-org `returnTo` on its own.

3. **§C — `web/dash0/src/routes/orgs/$org.tsx`.** Alongside `needsOrgSwitch`, compute
   `accessibleOrg = pickAccessibleOrg(org, auth)` and a `needsOrgRedirect` predicate
   (authenticated, not loading, not the login page, no OAuth token in the URL,
   `accessibleOrg !== org`). Its effect dedupes with a `redirectingForOrgRef` (at most one
   redirect per URL org), skips while `isSwitchOrgInFlight()`, navigates
   `replace: true` to `/orgs/$org` of the picked org (org root, never the sub-path) or to
   `/no-org`, and fires a `toast.warning` naming both slugs.

4. **Locales.** New key `org.accessRedirect.toast` (interpolating `{{requested}}` /
   `{{shown}}`) written into `en`, `fr`, `de`, `es` via a Python `OrderedDict` round-trip.

5. **§D — tests.** Unit file above; `e2e/demo-account.spec.ts` button test tightened to assert
   the demo org and `not.toContain("/orgs/test")` (run several times pre-fix to confirm it
   reproduces race (a)); new deterministic returning-demo-session test (no flag); new
   `e2e/accessible-org-redirect.spec.ts` covering the general non-member case with the
   create-org "no 403s" harness.

6. **QA.** `npx tsc -b`, scoped eslint (no NEW errors vs `HEAD`), `bun run test:unit`,
   `make build-dash0`, `bun run lint`, and the affected Playwright files against a side-car
   `SP_RUNMODE=test` server on port 4010.
