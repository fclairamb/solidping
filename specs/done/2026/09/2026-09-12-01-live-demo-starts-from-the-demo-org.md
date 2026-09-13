---
model: opus
effort: high
---

# "Try the live demo" enters through a foreign org and flashes "You don't have access to default — showing demo instead"

## Problem

Every click on the login page's **Try the live demo** button — and every
`?demo=true` deep link that resolves through `/d/login` or `/d/` — lands in the
demo org with a warning toast on top of it:

> ⚠️ You don't have access to default — showing demo instead.

The visitor never asked for `default`. They asked for the demo, got the demo,
and the first thing the product tells them is that something they did not do
was refused. The toast is the org layout's *non-member fallback*
([2026-09-08-01 §C](../done/2026/09/2026-09-08-01-redirect-to-an-accessible-org.md),
[`$org.tsx:1108-1115`](../../web/dash0/src/routes/orgs/$org.tsx)), built for a
bookmark to an org you were removed from or a link pasted from a colleague's
org. Firing it on the product's own front door is a bug in the entry flow, not
in the fallback.

### Why the demo is entered from a foreign org

The demo is never *started* from the demo org; it is started from whatever org
the URL happens to name, and then moved:

- The button is rendered on **every** org's login page
  ([`login.tsx:865-880`](../../web/dash0/src/routes/orgs/$org/login.tsx)).
  `enterDemo` signs in with `login(demoConfig.orgSlug, …)` while the URL is
  still `/orgs/<url org>/login`, then `routeResult(result, demoOrg)` navigates
  to `/orgs/<demo>` ([`login.tsx:330-370`](../../web/dash0/src/routes/orgs/$org/login.tsx),
  default case at `:304-307`).
- The published link `https://solidping.io/demo` is a 302 to
  `/d/login?demo=true` ([`server.go:2761`](../../server/internal/app/server.go));
  `LoginRedirect` turns that into `/orgs/<localStorage org || "default">/login?demo=true`
  ([`routes/login.tsx:40-55`](../../web/dash0/src/routes/login.tsx)). For a
  first-time visitor — the person the demo exists for — that org is literally
  `default`, which is where the toast's wording comes from.
- `RootRedirect` ([`routes/index.tsx:36-44`](../../web/dash0/src/routes/index.tsx))
  and the `$org` layout's `beforeLoad`
  ([`$org.tsx:115-121`](../../web/dash0/src/routes/orgs/$org.tsx)) do the same:
  keep the URL's org, add the flag. The `?demo` auto-login effect
  ([`login.tsx:385-409`](../../web/dash0/src/routes/orgs/$org/login.tsx)) then
  calls the same `enterDemo` from that foreign org's page.

So every entry path is "sign into `demo` while standing on `/orgs/default/login`,
then jump". Three earlier specs
([2026-09-06-02](../done/2026/09/2026-09-06-02-public-live-demo-account.md),
[2026-09-07-02](../done/2026/09/2026-09-07-02-demo-deep-link-and-own-check-edit.md),
[2026-09-08-01 §B/§D](../done/2026/09/2026-09-08-01-redirect-to-an-accessible-org.md))
each patched a race *produced by that jump*; this one removes the jump.

### Why the jump produces the toast (likely mechanism — confirm by reproducing)

`OrgLayout` decides whether to run the non-member fallback from two different
router reads: `isLoginPage` comes from `useLocation().pathname`
([`$org.tsx:1001`](../../web/dash0/src/routes/orgs/$org.tsx)), `org` from
`Route.useParams()` ([`$org.tsx:994`](../../web/dash0/src/routes/orgs/$org.tsx)).
In the router version in use (`@tanstack/react-router` 1.170,
[`web/dash0/package.json`](../../web/dash0/package.json)), `useLocation` reads
`router.stores.location`, which flips to the **pending** destination the moment
a navigation starts, while `useParams`/`useMatch` read the per-route match
store, which keeps the **committed** params until the new matches land
(`node_modules/@tanstack/react-router/dist/esm/useMatch.js`,
`useLocation.js`). Navigating `/orgs/default/login` → `/orgs/demo` therefore
commits at least one render where `isLoginPage === false` (pathname is already
`/orgs/demo`) and `org === "default"` (match not yet swapped).

In that render the demo session has just been applied
(`auth.org = "demo"`, `organizations = [demo]`, `isLoading` false — the login
payload carries the org list, so there is no `/auth/me` gap;
[`AuthContext.tsx:366-439`](../../web/dash0/src/contexts/AuthContext.tsx)),
so `pickAccessibleOrg("default", …)` returns `demo`, `needsAccessibleOrgRedirect`
is true ([`$org.tsx:1074-1088`](../../web/dash0/src/routes/orgs/$org.tsx)), and
the effect toasts and re-navigates to the place the router was already going.
Right outcome, wrong message — and deterministic, which matches "whenever we
click".

### Why the E2E suite does not catch it

[`demo-account.spec.ts:41-66`](../../web/dash0/e2e/demo-account.spec.ts) waits
for the **final** URL to match the demo org and checks the banner. A transient
toast plus a redundant navigation to the same URL passes. Nothing asserts the
toast's absence, and nothing records the intermediate URLs.

## Proposal

### A. The demo is only ever signed into from the demo org's own login page

One invariant: **`login(demoOrg, …)` runs only while the URL is
`/orgs/<demoOrg>/login`.** Every other entry point hops there first — a
`replace` navigation carrying `demo: true` — and lets that page do the sign-in.

- **The button on a foreign org's login page.** In `enterDemo`, when
  `org !== demoConfig.orgSlug`, navigate (`replace: true`) to
  `/orgs/$org/login` with `params.org = demoConfig.orgSlug` and
  `search.demo = true`, and do **not** call `login()`. The demo org's login
  page then enters through the existing `?demo` effect. On the demo org's own
  page the button keeps signing in directly. The already-in-demo short-circuit
  ([`login.tsx:339-346`](../../web/dash0/src/routes/orgs/$org/login.tsx)) stays
  as it is.
- **The `?demo` auto-login effect.** Same rule: on a foreign org's page, hop
  instead of logging in; on the demo org's page, log in. Keep the one-shot
  ref latch — the hop is a new page instance, so the latch resets naturally.
  `demoAutoLoginOwnsRedirect` ([`lib/demo.ts`](../../web/dash0/src/lib/demo.ts))
  is unchanged: the redirect effect must still stand down while the hop is
  pending.
- **The redirects that pick an org for the demo** (`LoginRedirect`,
  `RootRedirect`, the `$org` layout's `beforeLoad`) may keep their current
  target — the org login page will hop once more — but should prefer
  `demoConfig.orgSlug` when the public-config document is already in the
  query cache, so the common path is one hop rather than two. Do **not** make
  those redirects wait on the network; the second hop is the fallback, not the
  design.
- After this, `routeResult(result, demoOrg)` navigates from
  `/orgs/demo/login` to `/orgs/demo`: the layout's `org` param is `demo`
  before, during and after the transition, `pickAccessibleOrg` returns the
  URL's org, and the fallback has nothing to correct. The
  "redirect if already authenticated" effect and `routeResult` keep agreeing
  on the destination, as 2026-09-08-01 §B requires.
- Nothing else about the demo org's login page changes: the ordinary form on
  it still accepts the demo credentials, `returnTo` is still filtered by
  `resolveDestination`, and a visitor holding another org's session who
  follows a demo link still gets their session replaced without a
  confirmation (2026-09-07-02).

### B. Stop the org layout's fallback from reading a torn router snapshot

Defence in depth, and the fix for the same toast on any *other* app-initiated
cross-org navigation out of a login page (e.g. `pickAccessibleOrg` sending a
returning member from `/orgs/old/login` to `/orgs/theirs`). The fallback exists
for a visitor who **arrived** on a foreign org URL, not for a navigation the
app is already making.

- Derive `isLoginPage` and `org` from the **same** snapshot — either both from
  the matched routes (`useMatches()` containing the `/orgs/$org/login` /
  `/orgs/$org/register` route ids) or both from `useLocation()` — and say in a
  comment why the two reads must not be mixed. Alternatively (or additionally)
  gate `needsAccessibleOrgRedirect` and `needsOrgSwitch` on the router being
  idle (`useRouterState({ select: (s) => s.status })`), so neither guard
  evaluates against a half-committed transition.
- **Confirm the mechanism before fixing it**: reproduce with a demo-enabled
  server (`SP_DEMO_*`, see
  [`configuration/index.md`](../../web/docs/docs/configuration/index.md)) and
  either a Playwright toast watcher or a one-line render log of
  `{ pathname, org }` in `OrgLayout`. If the torn read is not what fires the
  toast, fix whatever does — A stands on its own either way.
- The legitimate case must keep toasting: a bookmark to an org you were
  removed from still shows "You don't have access to X — showing Y instead"
  (2026-09-08-01's existing test).

### C. Tests

Playwright, [`demo-account.spec.ts`](../../web/dash0/e2e/demo-account.spec.ts):

1. From `orgs/test/login`, click `login-demo`. Record every navigation
   (`page.on("framenavigated")` or a `waitForURL` chain) and assert the only
   URLs seen are the starting login page, `/orgs/<demo>/login…` and
   `/orgs/<demo>…` — never `/orgs/test` or any `/orgs/test/…` beyond the
   initial page. Assert the `accessRedirect.toast` text
   (`/don't have access to/i`) is **never** rendered: poll for its absence
   for a couple of seconds after the banner is visible rather than a single
   `not.toBeVisible`, which passes on a toast that has not appeared *yet*.
   Keep the existing "no 403" watcher.
2. The same assertions for `orgs/test/login?demo=1`, `login?demo=true`,
   `/?demo=true` and, against a demo-enabled server, the server-side `/demo`
   302.
3. Existing non-demo session in another org (sign in as the test user first),
   then follow `orgs/test/login?demo=1`: lands in the demo, banner visible, no
   toast.
4. Negative control for B: the non-member bookmark case still toasts.

Unit ([`demo.test.ts`](../../web/dash0/src/lib/demo.test.ts)): if the
"hop or sign in" decision is extracted into a pure helper in `lib/demo.ts`
(recommended — `(urlOrg, demoOrgSlug) → "signIn" | "hopTo" | "unavailable"`),
pin it there, including the `orgSlug`-not-yet-known case.

No published-docs change: the `/demo` link and its 302 are untouched. If the
wiki has a page for the live demo, add the invariant from §A to it.

### Out of scope

- Reworking the toast copy or the fallback's behaviour for genuine non-member
  arrivals.
- Making the `/demo` redirect itself org-aware on the server (it does not
  know the SPA's stored org and should not; the client hop covers it).

## Implementation Plan

1. **Confirm §B's mechanism first.** Stand up a side-car `SP_RUNMODE=test` server on an
   alternate port and its own database, add a temporary render log of
   `{ pathname, org, isLoginPage, accessibleOrg, needsAccessibleOrgRedirect }` to
   `OrgLayout`, and drive `orgs/test/login` → click `login-demo` with Playwright while
   capturing console + toasts. Record which of the two reads is ahead of the other before
   writing any fix. (`web/dash0/src/lib/org-public-routes.ts` documents the *opposite*
   tear for the org picker, so the direction is genuinely not knowable from the source.)

2. **§A — pure decision helper.** Add `demoEntryDecision(urlOrg, demoOrgSlug)` to
   `web/dash0/src/lib/demo.ts` returning `"signIn" | "hopTo" | "unavailable"`, with the
   `orgSlug`-not-yet-known case pinned in `demo.test.ts`.

3. **§A — the button and the `?demo` effect.** `enterDemo` in
   `web/dash0/src/routes/orgs/$org/login.tsx` keeps its already-in-demo short-circuit,
   then consults the helper: `"hopTo"` navigates (`replace: true`) to
   `/orgs/<demoOrg>/login?demo=true` and returns without calling `login()`; `"signIn"`
   does exactly what it does today. The auto-login one-shot latch becomes keyed by the
   URL org, so it re-arms after the hop whether or not the route component remounts.

4. **§A — one hop instead of two.** `LoginRedirect`, `RootRedirect` and the `$org`
   layout's `beforeLoad` prefer the demo org slug when the `publicConfig` query is
   **already** in the React Query cache, read synchronously — never awaited.

5. **§B — one snapshot.** Derive `isLoginPage` in `OrgLayout` from the committed
   **matches** (the same store `Route.useParams()` reads) rather than from
   `useLocation().pathname`, and additionally gate `needsAccessibleOrgRedirect` on the
   router being idle. Comment why the two reads must not be mixed.

6. **§C — tests.** Extend `web/dash0/e2e/demo-account.spec.ts` with a navigation
   recorder + a toast watcher that **polls for the toast's absence** for a couple of
   seconds after the banner is visible (a single `not.toBeVisible()` passes vacuously),
   across the button, `?demo=1`, `?demo=true`, `/?demo=true`, the `/demo` 302 and the
   existing-foreign-session case; plus a negative control proving the non-member bookmark
   still toasts, so the absence assertions are demonstrably able to fail.
