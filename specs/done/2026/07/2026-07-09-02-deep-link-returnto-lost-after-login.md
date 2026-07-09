# Deep link is lost after login — user lands on the org root instead of the original URL

## Problem

Clicking a deep link (e.g. a check detail or incident URL shared in Slack)
while not authenticated correctly redirects to the login page, but after a
successful login the user lands on the org dashboard root instead of the page
they originally asked for. The initial link is lost.

The `returnTo` plumbing exists and works on the *way in* — every
redirect-to-login carries the original URL:

- route guard: [web/dash0/src/routes/orgs/$org.tsx:100](web/dash0/src/routes/orgs/$org.tsx#L100)
  and the post-auth-resolve variant at
  [web/dash0/src/routes/orgs/$org.tsx:852](web/dash0/src/routes/orgs/$org.tsx#L852)
- 401 interceptor: [web/dash0/src/api/client.ts:139](web/dash0/src/api/client.ts#L139)

But on the *way out* of the login page, several success paths drop it. In
[web/dash0/src/routes/orgs/$org/login.tsx](web/dash0/src/routes/orgs/$org/login.tsx):

1. **`orgRedirect` login action** (`routeResult`,
   [login.tsx:302-306](web/dash0/src/routes/orgs/$org/login.tsx#L302)) —
   navigates to `/orgs/$org` root, ignoring `returnTo`. The backend returns
   `orgRedirect` when the user was silently redirected to their only available
   org ([server/internal/handlers/auth/service.go:226](server/internal/handlers/auth/service.go#L226)).
2. **Org picker** (`handleOrgSelect`,
   [login.tsx:446-462](web/dash0/src/routes/orgs/$org/login.tsx#L446)) — a
   multi-org user who picks an org always lands on that org's root; `returnTo`
   is ignored even when the picked org matches the org in the `returnTo` path.
3. **Already-authenticated effect**
   ([login.tsx:273-277](web/dash0/src/routes/orgs/$org/login.tsx#L273)) —
   when auth becomes valid while sitting on the login page (e.g. the boot-time
   refresh succeeds after the 401 interceptor hard-redirected here), it
   navigates to the org root, dropping `returnTo`. This effect also races the
   `window.location.href = returnTo` assignment in `routeResult`'s default
   case: it fires on the same `isAuthenticated` flip and issues an SPA
   `navigate` to the org root.

Only the `default` case of `routeResult`
([login.tsx:307-313](web/dash0/src/routes/orgs/$org/login.tsx#L307)) honors
`returnTo` today — i.e. a password/passkey/2FA login where the requested org
was directly resolved. The OAuth path forwards it as `redirect_uri`
([login.tsx:475-477](web/dash0/src/routes/orgs/$org/login.tsx#L475)); whether
the backend round-trips it to the final landing URL should be verified as part
of this spec.

## Proposal

Make every post-login success path resolve its destination through one shared
helper instead of hardcoding `/orgs/$org`:

- **Single resolver**: `resolveDestination(resolvedOrg): { href } | { to, params }`
  — returns `returnTo` when it is set, passes the safety guard, and its org
  segment matches the org actually logged into; otherwise the org root.
- **Apply it** in `routeResult`'s `orgRedirect` and `default` cases, in
  `handleOrgSelect`, and in the already-authenticated effect (the effect
  should send an authenticated visitor with a valid `returnTo` to that URL,
  not the org root).
- **Org mismatch rule**: if the org in `returnTo` differs from the org the
  session resolved to (the `orgRedirect` case, or picking a different org in
  the picker), fall back to that org's root — don't send the user to an org
  they didn't log into.
- **Tighten the guard**: replace `returnTo.includes("/orgs/")`
  ([login.tsx:308](web/dash0/src/routes/orgs/$org/login.tsx#L308)) with a
  same-origin relative-path check (`startsWith` on `basepath + "/orgs/"`,
  reject absolute URLs / `//host` forms) so it stays an open-redirect guard.
- **OAuth**: verify the backend propagates `redirect_uri` through the OAuth
  callback to the final in-app URL; fix if it drops it.

### Tests

- Playwright E2E (`web/dash0/e2e/`): visit a deep link (e.g.
  `/dash0/orgs/test/checks`) logged out → land on login with `returnTo` →
  log in → assert final URL is the original deep link, including query
  string. Cover the plain password flow at minimum; the org-picker flow if a
  multi-org fixture is available.
- Unit-level coverage for the destination resolver (returnTo honored, org
  mismatch falls back, absolute/`//` URLs rejected).

### Open questions

- Should the org picker rewrite the org segment of `returnTo` when the user
  picks a different org (best-effort deep link into the sibling org), or
  always fall back to the picked org's root? Default to the simpler fallback.

## Implementation Plan

### 1. Shared destination resolver (pure, unit-tested)
Add `web/dash0/src/lib/login-destination.ts` exporting:
- `type LoginDestination = { href: string } | { to: "/orgs/$org"; params: { org: string } }`.
- `resolveDestination(resolvedOrg, returnTo, basepath): LoginDestination` — returns
  `{ href: returnTo }` only when `returnTo` is safe (`isSafeReturnTo`) **and** its org
  segment equals `resolvedOrg`; otherwise the org root `{ to, params }`.
- `isSafeReturnTo(returnTo, basepath)` — rejects protocol-relative (`//host`), backslash
  (`/\`) and scheme'd absolute URLs (`https:`, `javascript:`…); requires
  `returnTo.startsWith(basepath + "/orgs/")`.
- `returnToOrg(returnTo, basepath)` — strips query/hash + basepath, reads the slug after
  `/orgs/`.
Colocated `login-destination.test.ts` (vitest): returnTo honored on org match; org
mismatch → fallback; absolute `http(s)://` and protocol-relative `//host` rejected;
missing/empty returnTo → fallback.

### 2. Wire the resolver into `login.tsx`
- Module const `BASE_PATH = import.meta.env.VITE_BASE_URL || ""` (build-time constant).
- `goToDestination(dest, replace?)` callback: `href` → `window.location.href`/`.replace`;
  `to` → `navigate({ to, params, replace })`.
- Apply in the four success paths:
  - `routeResult` **orgRedirect** case → `goToDestination(resolveDestination(result.resolvedOrg, returnTo, BASE_PATH))`.
  - `routeResult` **default** case → same with `resolvedOrg = result.resolvedOrg || org`
    (replaces the loose `returnTo.includes("/orgs/")` guard).
  - `handleOrgSelect` → `goToDestination(resolveDestination(orgSlug, returnTo, BASE_PATH))`
    (org-mismatch falls back to the picked org's root — the Open-questions default).
  - already-authenticated effect → `goToDestination(resolveDestination(org, returnTo, BASE_PATH), true)`
    (send an authenticated visitor with a valid returnTo to it, not the org root; `replace`
    keeps /login out of history and both this effect and the default case now agree, killing
    the race).
- Keep hook dependency arrays exhaustive (no new eslint warnings).

### 3. Backend OAuth verification (no code change expected)
Confirmed: each provider's login handler stores `redirect_uri` in the OAuth state and the
callback's `buildSuccessRedirect(state.RedirectURI, result)` `url.Parse`s it, preserves its
query string, and appends the tokens — so a deep-link `redirect_uri` round-trips to the final
in-app URL. No backend change required; `make build-backend` run to confirm compilation.

### 4. E2E
Extend `web/dash0/e2e/` with a deep-link spec: logged-out visit to
`/dash0/orgs/test/checks` → login page carrying `returnTo` → password login → assert final
URL is the original deep link (path + query). Authored regardless; run locally only if the
`:4000` devloop is in `SP_RUNMODE=test`.
