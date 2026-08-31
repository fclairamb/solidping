---
model: sonnet
effort: medium
---

# An unauthenticated visitor to `/orgs/:org/register` is bounced to the login page with "session expired"

## Problem

`OrgLayout` (`web/dash0/src/routes/orgs/$org.tsx`, function around `:970`) works out
whether it is rendering a public route:

```ts
const isLoginPage = isOrgPublicRoute(location.pathname);   // $org.tsx:974
const { data: features } = useFeatures();                  // $org.tsx:977
```

…and then never uses that flag to gate the query. `useFeatures()`
(`web/dash0/src/api/hooks.ts` ~`:3890`) fires unconditionally, on every render of the
org layout, including on the two public routes.

`GET /api/v1/features` is authenticated — `server/internal/app/server.go:1420`:

```go
api.NewGroup("/features").Use(authMiddleware.RequireAuth).GET("", featuresHandler.GetFeatures)
```

So for a visitor with no token the call 401s, and `handleResponse` in
`web/dash0/src/api/client.ts` reacts to a 401 by calling `redirectToExpiredLogin()`,
which navigates to `/orgs/:org/login?session_expired=true&returnTo=…`.

**Why only `/register` visibly breaks.** `redirectToExpiredLogin`
(`web/dash0/src/api/client.ts:195-197`) opens with:

```ts
if (currentPath.endsWith("/login")) return;
```

On `/orgs/:org/login` the redirect is a no-op, so the 401 is invisible. On
`/orgs/:org/register` — equally public, per `isOrgPublicRoute`
(`web/dash0/src/lib/org-public-routes.ts`, which matches
`/\/orgs\/[^/]+\/(login|register)$/`) — there is no such guard, so the visitor is
thrown off the sign-up form before they can use it, and told their session expired
when they never had one.

**Reproduced live** (2026-08-29): a fresh browser tab with no `localStorage` token at
`http://localhost:4020/dash0/orgs/test/register` (side-car, `SP_RUNMODE=test`) lands
on the login page showing "Your session has expired. Please log in again." The network
trace shows `GET /api/v1/features` → 401, immediately followed by the redirect.

This makes org-scoped self-registration effectively unreachable by direct link, which
is exactly how an invited or self-serve user arrives at it.

Found incidentally while writing the E2E for spec
`2026-08-29-06-confirm-registration-no-org-logged-out`; unrelated to that spec's fix.

## Proposal

Gate the query on the layout's own public-route flag, which is already computed one
line above it:

```ts
const { data: features } = useFeatures({ enabled: !isLoginPage });
```

`useFeatures` currently takes no options, so it needs an optional
`{ enabled?: boolean }` parameter threaded into its `useQuery` call — mirroring the
`ListQueryOptions`/`enabled` pattern other hooks in `web/dash0/src/api/hooks.ts`
already use.

**Verify the consumers before assuming this is safe.** `features` is read in three
places in `$org.tsx`:

- `:978` — `const feedback = useFeedback({ enabled: features?.bugReport === true, org });`
- `:1103` and `:1109` — `{features?.bugReport && …}` render gates for the feedback button.

All three already treat `undefined` as "off": with the query disabled, `features` is
`undefined`, `features?.bugReport === true` is `false`, and the buttons don't render.
That is the correct behaviour on a public page — an anonymous visitor should not be
offered the bug-report button. Confirm this in the code rather than taking it on
faith, and confirm the feedback button still appears normally for an authenticated
user once the query is enabled again.

**Sweep for siblings.** The bug is not really "`useFeatures` is ungated" but "an
authenticated query runs on a public route, and a 401 there is indistinguishable from
an expired session". Check whether any other hook called from `OrgLayout` (or from a
component it renders on the public branch) hits an authenticated endpoint without an
`enabled` gate, and fix those in the same pass. If there are none, say so.

**Consider hardening the redirect too, as defence in depth** (implementer's call —
keep it small, and note the decision): `redirectToExpiredLogin` special-cases
`/login` but not `/register`, even though `isOrgPublicRoute` already knows both are
public. Reusing `isOrgPublicRoute` there instead of the bare `endsWith("/login")`
would make any *future* stray authenticated call on a public route harmless rather
than user-visible. Do not let this replace the real fix — a 401-triggered redirect
loop suppressed at the redirect is still a spurious request.

## Tests

- **E2E (Playwright, `web/dash0/e2e/`)**: visit `/orgs/:org/register` with **no**
  stored token and assert the page stays on `/register` and renders the sign-up form
  — no navigation to `/login`, no `session_expired` param. This must prove the
  negative; a test that merely visits the page and passes because the redirect is
  slow is worthless, so assert on the settled URL and on a form element being visible.
  Extend the existing login/register E2E coverage rather than adding a new file if one
  fits.
- **Unit**: if `useFeatures` gains an options parameter, cover that `enabled: false`
  issues no request.
- Existing suites must stay green — in particular anything asserting the feedback
  button appears for authenticated users.

## Out of scope

- Changing what `/api/v1/features` returns, or making it public. It is authenticated
  on purpose; the fix is not to call it when unauthenticated.
- Reworking `handleResponse`'s general 401 handling beyond the optional
  public-route hardening noted above.
