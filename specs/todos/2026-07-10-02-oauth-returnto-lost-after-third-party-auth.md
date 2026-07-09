# Third-party (OAuth/SSO) login discards `returnTo`, always landing on the org root

## Problem

When an unauthenticated user hits a deep page (e.g. `/dash0/orgs/acme/checks/foo`),
the API client redirects them to login with `?returnTo={currentPath}`
([`web/dash0/src/api/client.ts:134`](web/dash0/src/api/client.ts:134)). Password,
passkey and org-picker logins all honor that `returnTo` and send the user back to
where they started. **Third-party auth (GitHub, Google, OIDC, Microsoft, GitLab,
Discord) does not** — after the provider round-trip the user always lands on
`/dash0/orgs/{org}`, losing their original destination.

The surprising part is that `returnTo` survives almost the entire flow:

1. The login page passes it through as `redirect_uri` on the OAuth login call
   ([`login.tsx:502`](web/dash0/src/routes/orgs/$org/login.tsx:502) — `handleOAuthLogin`).
2. The backend stores it in the OAuth `state`
   ([`server/internal/handlers/auth/github.go:45`](server/internal/handlers/auth/github.go:45)),
   recovers it in the callback ([`github.go:81`](server/internal/handlers/auth/github.go:81)),
   and builds the success redirect on top of it
   ([`buildSuccessRedirect`, `github.go:111`](server/internal/handlers/auth/github.go:111)) —
   appending `access_token`, `refresh_token`, `expires_in` and `org`. All social
   providers share this shape (`google.go`, `oidc.go`, `microsoft.go`, `gitlab.go`,
   `discord.go`).
3. The browser actually lands on the correct deep path with tokens appended, e.g.
   `/dash0/orgs/acme/checks/foo?access_token=…&org=acme`.

Then it's thrown away. The pre-React OAuth-handoff IIFE in
[`web/dash0/src/main.tsx:29`](web/dash0/src/main.tsx:29) runs before the router
mounts and rewrites the URL:

```ts
const dest = handoff.org ? `${basepath}/orgs/${handoff.org}` : window.location.pathname;
window.history.replaceState(null, "", dest);   // <-- drops the deep path
```

Because `buildSuccessRedirect` always sets the `org` query param
([`github.go:121`](server/internal/handlers/auth/github.go:121)), `handoff.org` is
always truthy, so the ternary unconditionally replaces the (already-correct)
`window.location.pathname` with the org root. [`main.tsx:36`](web/dash0/src/main.tsx:36)
is the single line that loses the original destination.

The other login paths avoid this because they funnel through
`resolveDestination()` ([`web/dash0/src/lib/login-destination.ts:28`](web/dash0/src/lib/login-destination.ts:28)),
which honors `returnTo` when it's safe/same-origin/org-matching and only falls back
to the org root otherwise. The `main.tsx` handoff never calls it — it makes its own
naive decision because it runs before React.

## Proposal

Make the `main.tsx` OAuth handoff preserve the deep path instead of unconditionally
rewriting to the org root, reusing the same guards as every other login path.

At [`main.tsx:36`](web/dash0/src/main.tsx:36), replace the naive ternary with a call
to `resolveDestination(handoff.org, window.location.pathname, basepath)` (or the
equivalent) so the handoff inherits the existing safe-path / same-origin / org-match
checks from [`login-destination.ts`](web/dash0/src/lib/login-destination.ts). The
current `window.location.pathname` already equals the honored `returnTo` the backend
redirected to, so the fix is essentially: keep it when it passes the guards, fall
back to `/orgs/{org}` when it doesn't.

Notes / open questions:
- Confirm `resolveDestination` is importable this early (it must not pull in the
  router or other not-yet-mounted modules). If it does, factor out the pure
  safe-path/org-match helpers so the handoff can use them standalone.
- The comment at [`oauth-handoff.ts:14`](web/dash0/src/lib/oauth-handoff.ts:14) notes
  `OrgLayout` has a second, equivalent handoff effect that never fires because this
  IIFE strips the params first — worth confirming it stays dormant (or is removed)
  after the fix.
- Verify the fix across all providers, not just GitHub, since they share
  `buildSuccessRedirect`. `discord.go` defaults `redirect_uri` to `/` rather than the
  org dashboard — check that path too.

### Test coverage
- Add a dash0 E2E (or unit) test that starts on a deep page, goes through the
  (mocked) OAuth handoff with `access_token` + `org` + a deep `returnTo` path, and
  asserts the final URL is the deep path, not `/orgs/{org}`.
- Add a negative test: an unsafe/cross-org `returnTo` still falls back to the org
  root.
