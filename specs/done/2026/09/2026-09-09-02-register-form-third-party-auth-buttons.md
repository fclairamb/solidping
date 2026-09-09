---
model: opus
effort: high
---

# The register form should offer every sign-in mechanism the login form offers

## Problem

The login page and the register page disagree about how you can get an account.

The login page ([`web/dash0/src/routes/orgs/$org/login.tsx`](../../web/dash0/src/routes/orgs/$org/login.tsx))
renders a two-column grid of brand-iconed buttons for every configured provider
(`login.tsx:968-999`), a promoted "last used" slot above it (`login.tsx:910-966`),
a passkey button (`login.tsx:1070-1083`) and then the password form. The register
page ([`web/dash0/src/routes/orgs/$org/register.tsx`](../../web/dash0/src/routes/orgs/$org/register.tsx))
renders name / email / password and nothing else (`register.tsx:96-181`).

That gap is purely a frontend one. On the backend there is no such thing as a
separate "sign up with Google" endpoint: every provider callback runs
`findOrCreateUser` and creates the account on first sight —
[`oidc_service.go:302`](../../server/internal/handlers/auth/oidc_service.go) /
`:378-432` is the canonical shape, and the same helper exists in all eight
provider services (`google`, `github`, `gitlab`, `microsoft`, `discord`,
`slack`, `oidc`, `saml`), each recording its own `signupMethod`
([`signup_analytics.go:20-35`](../../server/internal/handlers/auth/signup_analytics.go)).
So a first-time "Continue with GitHub" **is** registration. The only place a
visitor can click it is a page titled "Sign in", under a heading that reads as
"for existing users", after they have first noticed the small "Already have an
account? Sign in" link at the bottom of `/register`. Visitors who arrive on
`/register` from the marketing site and want to use their Google account either
figure this out or type a password they did not want.

Two things make the obvious fix ("copy the grid over") wrong:

1. **The icons and the provider→icon map are private to the login route.**
   `GoogleIcon`, `SlackIcon`, `GitHubIcon`, `MicrosoftIcon`, `GitLabIcon`,
   `DiscordIcon`, `OIDCIcon`, `SAMLIcon` and the `PROVIDER_ICONS` record live at
   `login.tsx:89-253`; nothing else in `web/dash0/src` references them. Reusing
   them means either copy-pasting ~165 lines of SVG or extracting them.
2. **The redirect logic is not trivial.** `handleOAuthLogin` (`login.tsx:737-758`)
   records the last-used method, handles the MCP OAuth consent bounce
   (`isOAuthAuthorizeReturnTo`), strips stale `?error=` params, and builds
   `/api/v1/auth/<type>/login?org=…&redirect_uri=…`. A second hand-rolled copy
   on the register page would drift from it.

Two smaller observations, worth fixing on the way since the register page will
need the same data:

- The login page fetches `/api/v1/auth/providers` **twice** — once through
  `useProviders()` (`login.tsx:278`, [`hooks.ts:3409-3424`](../../web/dash0/src/api/hooks.ts))
  for the provider list and `registrationEnabled`, and once more through
  `getAuthProviders()` (`login.tsx:301`, [`passkeys.ts:116`](../../web/dash0/src/api/passkeys.ts))
  just to read `passkeysEnabled`. The response carries all three fields
  ([`providers_available.go:40-42`](../../server/internal/handlers/auth/providers_available.go));
  the hook simply drops one.
- There is no `web/dash0/src/components/auth/` directory yet — auth UI has so
  far lived inline in the two routes.

## Proposal

### 1. Extract the provider icons into a shared component

Move the eight icon components and the `PROVIDER_ICONS` map out of
`login.tsx:89-253` into `web/dash0/src/components/auth/provider-icon.tsx`,
exporting a single `ProviderIcon({ type, className })` that resolves the map and
renders nothing for an unknown type (today's `Icon && <Icon …/>` guard). Keep
the existing comments on `OIDCIcon` / `SAMLIcon` explaining why those two are
neutral glyphs rather than brand marks — they are the design rationale.

### 2. Extract the OAuth button grid, and the URL it navigates to

Add `web/dash0/src/components/auth/oauth-provider-buttons.tsx` rendering exactly
what `login.tsx:968-999` renders today — the two-column `grid grid-cols-2 gap-2`
of `variant="outline" size="sm"` buttons with `ProviderIcon` + provider name,
followed by the "or" divider — driven by props rather than route state:

```ts
interface OAuthProviderButtonsProps {
  org: string;
  providers: AuthProvider[];      // already filtered by the caller
  disabled?: boolean;
  returnTo?: string;              // login passes its search param; register omits it
  testIdPrefix: "login" | "register";
}
```

Move the body of `handleOAuthLogin` (`login.tsx:737-758`) into a pure helper —
`buildOAuthLoginUrl({ org, providerType, returnTo })` in
`web/dash0/src/lib/login-destination.ts`, next to the `isOAuthAuthorizeReturnTo`
/ `stripOAuthErrorParams` helpers it already depends on — and have the component
call `setLastAuthMethod` then assign `window.location.href`. The login page's
promoted "last used" slot (`login.tsx:910-966`) keeps its own markup (it carries
the badge and the `-promoted` test id) but must go through the same helper and
`ProviderIcon`, so there is exactly one place that knows the redirect URL.

Rendering must be pixel-identical on the login page after the extraction. The
existing promotion e2e tests (`login.spec.ts:425-458` and neighbours) are the
regression guard; do not loosen them.

### 3. Render the grid on the register page

In `register.tsx`, above the `<form>` and inside the same `CardContent`, render
`<OAuthProviderButtons org={org} providers={providers} disabled={register.isPending} testIdPrefix="register" />`
when at least one provider is configured. Same buttons, same labels (the bare
provider name — "Google", "GitHub"), same "or" divider, then the existing
name / email / password fields. The card heading "Create your account" is what
tells the visitor these buttons create an account; no extra copy, no new locale
keys, so the four locales (`de en es fr`, enforced by
`web/dash0/src/locales/locale-parity.test.ts`) stay untouched.

Deliberate differences from the login page, each with a reason:

- **No "last used" promoted slot.** A visitor on `/register` is claiming to be
  new; promoting a remembered provider is the wrong signal here. The grid still
  calls `setLastAuthMethod("oauth:<type>")` on click (it comes with the shared
  handler), so the *next* login promotes the provider they signed up with.
- **No passkey button.** Passkey enrolment is an authenticated operation —
  `/passkeys/register/begin|finish` are mounted on `rootAuthProtected`
  ([`server.go:713-714`](../../server/internal/app/server.go)) — so there is
  nothing a passkey could do for someone without an account yet. Passkeys are
  added from account settings after the first login. Do not fake a
  "Sign up with passkey" button that lands on a login ceremony.
- **The grid is not gated on `registrationEnabled`.** That flag mirrors the
  `auth.registration_email_pattern` config and only gates *password*
  self-registration ([`service.go:2337-2339`](../../server/internal/handlers/auth/service.go));
  `findOrCreateUser` never consults it, and the login page already shows the
  same buttons regardless. The register page follows the backend, not the
  password form. (Whether OAuth signups *should* be gated is a separate,
  pre-existing question — see Out of scope.)

After the provider callback the visitor lands on `${DASH_BASE}/orgs/${org}`
exactly as they would from the login page, and the org join policy
(`join_policy.go`) applies as it does today.

### 4. Stop fetching `/auth/providers` twice

Add `passkeysEnabled` to `ProvidersResponse` / the `useProviders()` return value
([`hooks.ts:3399-3424`](../../web/dash0/src/api/hooks.ts)) and drop the second
`getAuthProviders()` effect in `login.tsx:296-310`. `getAuthProviders` in
`passkeys.ts` can go if nothing else imports it. The register page then needs
only `useProviders()`.

### 5. Design reference

Per the frontend rule in `CLAUDE.md`, add `OAuthProviderButtons` (with a sample
provider list showing every icon) to
[`web/dash0/src/routes/orgs/$org/design-reference.tsx`](../../web/dash0/src/routes/orgs/$org/design-reference.tsx)
with its import line, so the next auth surface reuses it instead of inlining.

### Tests

- **Unit** (`web/dash0/src/lib/login-destination.test.ts` or alongside):
  `buildOAuthLoginUrl` — plain org destination, `returnTo` carrying stale
  `?error=` params (stripped), and an MCP `/oauth/authorize` `returnTo` (kept
  verbatim). These are the three branches of today's `handleOAuthLogin`.
- **E2E** (`web/dash0/e2e/login.spec.ts` already has `fetchAuthCapabilities` at
  `:256` and the `test.skip` pattern for "no provider configured"; a new
  `register-oauth-buttons.spec.ts` may import it or move it to `fixtures.ts`):
  - every provider in `/api/v1/auth/providers` renders a
    `register-oauth-<type>` button on `/orgs/test/register`, in the same order
    as the `login-oauth-<type>` buttons on `/orgs/test/login`;
  - clicking one navigates to `/api/v1/auth/<type>/login?org=test&redirect_uri=…`
    (intercept with `page.route` and assert the request URL — the real provider
    is not reachable from CI);
  - with `LAST_AUTH_METHOD_KEY` pre-seeded to `oauth:<type>` in `localStorage`,
    `/register` shows **no** `login-last-used` slot and keeps the provider in
    the grid (proves the negative — the promotion logic must not leak in);
  - with passkeys enabled on the test backend, `/register` shows no
    `passkey-login-button`;
  - the existing "Register: public route stays public" test (`login.spec.ts:181`)
    and the three promotion tests (`:349`, `:425`, `:459`) stay green unchanged.
- `bun run test:unit` for locale parity (no new keys expected — the test proves
  it).

### Out of scope, stated so nobody reinvents it mid-implementation

- **Signup attribution for OAuth signups.** `readSignupAttribution()` is only
  wired into the password `POST /auth/register`; the round trip through the
  provider is a full reload that drops the in-memory tags, and
  [`attribution.ts:19-21`](../../web/dash0/src/lib/attribution.ts) already
  declares that a separate spec. The register-page buttons use the same handler
  as login and therefore carry no attribution. Do not bolt a query param onto
  the redirect URL here.
- **Gating OAuth signups on `registrationEnabled` / the email pattern.** Today
  a configured provider creates accounts unconditionally, on both pages. Whether
  that is the intended policy is a backend question that predates this spec.
- **A buttons-only register page when password registration is disabled.**
  With the shared component this becomes a one-line conditional
  (`registrationEnabled ? <form/> : null`); worth doing, but it changes what
  `/register` means and deserves its own decision.

## Implementation Plan

File-by-file breakdown of §1–§5.

### §1 — `web/dash0/src/components/auth/provider-icon.tsx` (new)

Move `GoogleIcon`, `SlackIcon`, `GitHubIcon`, `MicrosoftIcon`, `GitLabIcon`,
`DiscordIcon`, `OIDCIcon`, `SAMLIcon` and `PROVIDER_ICONS` verbatim out of
`login.tsx:89-253` (SVG paths unchanged, and the `OIDCIcon` / `SAMLIcon`
rationale comments carried over word for word). Export
`ProviderIcon({ type, className })`, which resolves `PROVIDER_ICONS[type]` and
returns `null` for an unknown type — the same behaviour as today's
`Icon && <Icon …/>` guard, moved inside the component so callers no longer
repeat it.

### §2 — the shared button grid and the one place that knows the URL

- `web/dash0/src/lib/login-destination.ts`: add
  `buildOAuthLoginUrl({ org, providerType, returnTo })`, a pure function
  holding the body of `handleOAuthLogin` minus the `setLastAuthMethod` side
  effect. Three branches, unchanged: MCP `/oauth/authorize` `returnTo` → wrap
  it in a `…/orgs/<org>/login?returnTo=…` redirect_uri; any other `returnTo` →
  `stripOAuthErrorParams`; no `returnTo` → `${DASH_BASE}/orgs/<org>`. It uses
  `DASH_BASE` where `login.tsx` used its local `BASE_PATH`; both are
  `import.meta.env.VITE_BASE_URL` at build time (vite.config.ts always
  `define`s it), so the emitted URL is byte-identical — the difference only
  shows under vitest, where `DASH_BASE` is the honest value.
- `web/dash0/src/components/auth/oauth-provider-buttons.tsx` (new): the
  `grid grid-cols-2 gap-2` of `variant="outline" size="sm"` buttons plus the
  "or" divider, lifted from `login.tsx:968-999` with the same class names and
  the same `mb-3` wrapper. Props exactly as the spec's interface. Renders
  `null` when `providers` is empty. Its click handler is
  `setLastAuthMethod("oauth:<type>")` then
  `window.location.href = buildOAuthLoginUrl(...)`.
- `login.tsx`: delete the icons, `PROVIDER_ICONS` and the body of
  `handleOAuthLogin`; keep a thin `handleOAuthLogin` for the promoted slot that
  calls the same `setLastAuthMethod` + `buildOAuthLoginUrl` pair, and render
  the grid as `<OAuthProviderButtons … testIdPrefix="login" />`. The promoted
  slot keeps its own markup (badge + `-promoted` test id) and switches to
  `<ProviderIcon type={…} className="mr-2 h-4 w-4" />`.

### §3 — `web/dash0/src/routes/orgs/$org/register.tsx`

Call `useProviders()`, and render
`<OAuthProviderButtons org={org} providers={providers} disabled={register.isPending} testIdPrefix="register" />`
as the first child of `CardContent` after the error alert, above the `<form>`.
No promoted slot, no passkey button, no `registrationEnabled` gate — the three
deliberate differences, each already argued in the Proposal. No new copy, so no
new locale keys.

### §4 — one `/auth/providers` fetch

`hooks.ts`: add `passkeysEnabled?: boolean` to `ProvidersResponse` and
`passkeysEnabled: response.passkeysEnabled || false` to the `useProviders()`
return. `login.tsx`: drop the `passkeysEnabled` `useState` + the
`getAuthProviders()` effect and read the flag off `providersData` instead.
`passkeys.ts`: delete `getAuthProviders` and `AuthProvidersResponse` once the
grep shows `login.tsx` was the only importer.

### §5 — design reference

Add an `OAuthProviderButtons` section (nav entry + `Section` + `ExampleRow`)
rendering a sample list covering all eight provider types, so every icon is on
the page, with the import line.

### Tests

- `src/lib/login-destination.test.ts`: three `buildOAuthLoginUrl` cases (plain
  org destination, `returnTo` with stale `?error=`, MCP authorize `returnTo`).
- `e2e/register-oauth-buttons.spec.ts` (new): the five bullets from the Tests
  section. `fetchAuthCapabilities` moves from `login.spec.ts` into
  `e2e/fixtures.ts` so both specs share it.
- `bun run test:unit` proves locale parity is untouched.
