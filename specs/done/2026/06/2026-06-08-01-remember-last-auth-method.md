# Remember the last authentication method and propose it first

## Context
Returning users on orgs with multiple sign-in methods (several OAuth providers,
or a mix of OAuth + passkey + password) currently have to re-find their method on
every visit — the login page shows all options in a fixed, hardcoded order with
nothing indicating which one they used before. Tools like polar.sh, Slack and
Google's account chooser remove this friction by remembering the last method and
floating it to the top with a small "Last used" marker.

This spec adds that behaviour to the dash0 login page. It is a client-only,
`localStorage`-based enhancement — no API or backend changes. Honest scoping note:
the payoff is proportional to how many *alternative* methods an org has configured.
For the default email+password-only setup it is nearly invisible; it earns its keep
on orgs with 2+ providers (or provider + passkey). Treat this as polish for
returning users, not a high-impact feature.

## Goal
On the login page, the authentication method used last is promoted to the top and
tagged with a tiny "Last used" badge, so a returning user can re-authenticate in
one glance/click. Memory is a single global per-browser value.

## Behaviour

Tracked methods and stored values (single global key, no org in the key):
- OAuth provider → `oauth:<type>` (e.g. `oauth:google`)
- Passkey → `passkey`
- Email + password → `password`

`localStorage` key: `solidping_last_auth_method`.

### When last-used is an OAuth provider (and that provider is still configured)
- Render a **promoted full-width "Continue with <Name>" button at the very top** of
  the login card (above the existing provider grid), with the brand icon and a small
  "Last used" `Badge`. Clicking it runs the existing `handleOAuthLogin(type)`.
- **De-duplicate:** remove that provider from the normal grid below so it is not
  listed twice. The rest of the grid, the "OR" divider, the password form and the
  passkey button render unchanged.

### When last-used is passkey (and passkeys are enabled + browser supports WebAuthn)
- Render the passkey button as the **promoted top element** with the "Last used"
  badge, wired to the existing `handlePasskeyLogin`.
- Hide the duplicate passkey button at the bottom of the form.

### When last-used is password
- The email+password form is already the always-visible primary option, and a
  multi-field form cannot collapse into a single promoted button — so **no top slot**
  is added. Instead:
  - show the "Last used" badge on the form (next to the "Sign in" submit button), and
  - autofocus the email field on load.

### No / stale memory
- No stored value, or the stored method is **not currently available** (provider
  removed from the org, passkey disabled) → render the default layout with no
  promoted slot and no badge. Never promote an unavailable method.

### Recording the choice
- `handleOAuthLogin(type)`: write `oauth:<type>` **immediately before** the redirect
  (success can't be observed — we record intent).
- `handleSubmit` (password): write `password` when `login()` resolves without
  throwing (correct password, even if a 2FA step follows).
- `handlePasskeyLogin` and the conditional-UI autofill success path: write `passkey`
  after a successful ceremony.

## Implementation outline

**New helper** `web/dash0/src/lib/last-auth-method.ts`
- `getLastAuthMethod(): string | null` and `setLastAuthMethod(method: string): void`.
- Const key `solidping_last_auth_method`. Guard with `typeof window === "undefined"`
  and wrap access in try/catch (private mode / disabled storage), mirroring the
  existing patterns in `routes/login.tsx` and `components/dashboard/dashboard-page.tsx`.
- Follows the existing co-located typed getter/setter convention (cf. `getToken`/
  `setToken` in `api/client.ts`, `getStoredOrg`/`setStoredOrg` in
  `contexts/AuthContext.tsx`).

**Login page** `web/dash0/src/routes/orgs/$org/login.tsx`
- Read `getLastAuthMethod()` once into state/memo.
- Reuse the existing `PROVIDER_ICONS` map and `Badge` (`@/components/ui/badge`,
  `variant="secondary"`, small/inline) for the badge.
- Add the promoted-slot block at the top of the existing non-2FA / non-org-picker
  branch (the `<>` starting ~line 530). Filter the promoted provider out of the
  `providers.map(...)` grid (~line 534). Conditionally hide the bottom passkey button
  (~line 609) when passkey is promoted. Add the badge + autofocus to the password
  form for the password case.
- Add `setLastAuthMethod(...)` calls in `handleOAuthLogin` (~line 392), `handleSubmit`
  (~line 270, on success), `handlePasskeyLogin` (~line 304, on success) and the
  conditional-UI effect success path (~line 332).
- New test IDs: `login-last-used` (promoted slot) and `login-last-used-badge`.

**i18n** — add `"lastUsed": "Last used"` to the `auth` namespace in all four locales:
`web/dash0/src/locales/{en,de,es,fr}/auth.json` (sibling of `signIn`).

**Design reference** `web/dash0/src/routes/orgs/$org/design-reference.tsx`
- Per the canonical-catalog convention, add an example to the "Buttons & badges"
  section showing a button carrying a "Last used" badge (the promoted-slot pattern),
  with its import line.

## Out of scope
- Any server / API change (`/auth/providers` stays as-is). No new endpoint or column.
- Server-driven or per-account "last used" (this is per-browser only).
- Reordering the OAuth provider grid beyond pulling the single last-used provider
  into the promoted slot.

## Testing
Playwright E2E in `web/dash0/e2e/` (preferred over Chrome MCP), extending
`e2e/login.spec.ts` patterns (`orgs/test/login`, `networkidle`, test IDs).

Deterministic, always-runnable:
- **Password rendering:** `page.addInitScript` to set
  `localStorage.solidping_last_auth_method = "password"`, load `orgs/test/login`
  unauthenticated, assert `login-last-used-badge` is visible near the form and the
  email field is focused.
- **No memory:** with storage cleared, assert no `login-last-used` slot and no badge.
- **Recording:** log in as the test user (`test@test.com` / `test`, org `test`),
  then assert `localStorage.solidping_last_auth_method === "password"` via
  `page.evaluate`.

Config-dependent (assert if the test backend has the capability, otherwise skip +
cover by manual browser verification):
- **OAuth promotion:** seed `oauth:<configuredType>`, assert the promoted
  "Continue with <Name>" button + badge renders at top and the provider is removed
  from the grid.
- **Passkey promotion:** seed `passkey` with passkeys enabled, assert the promoted
  passkey button + badge (WebAuthn ceremony itself not exercised).

Manual verification: `make dev-test`, open `http://localhost:4000/dash0/orgs/test/login`,
sign in with each available method, return to the login page, confirm the method is
promoted with the "Last used" badge and that switching methods updates the memory.
Also confirm the new design-reference entry at
`http://localhost:4000/dash0/orgs/default/design-reference`.

## Implementation Plan

1. **Helper module** `web/dash0/src/lib/last-auth-method.ts`
   - Const `LAST_AUTH_METHOD_KEY = "solidping_last_auth_method"`.
   - `getLastAuthMethod(): string | null` — SSR guard (`typeof window === "undefined"`)
     + try/catch (private mode / disabled storage), returns `null` on any failure.
   - `setLastAuthMethod(method: string): void` — same guards, swallows write errors.
   - Mirrors `getToken`/`setToken` (`api/client.ts`) and the FirstResultCelebration
     localStorage pattern (`components/dashboard/dashboard-page.tsx`).

2. **Login page** `web/dash0/src/routes/orgs/$org/login.tsx`
   - Import helper + `Badge` (`@/components/ui/badge`).
   - Read `getLastAuthMethod()` once via `useState(() => ...)`.
   - Compute `promotedProvider` (last-used `oauth:<type>` still present in `providers`)
     and `promotePasskey` (last-used `passkey` and passkeys enabled + browser supports
     WebAuthn). Never promote an unavailable method.
   - Render a promoted top slot (`data-testid="login-last-used"`) inside the non-2FA /
     non-org-picker branch:
     - OAuth: full-width "Continue with <Name>" button + brand icon + "Last used"
       `Badge` (`data-testid="login-last-used-badge"`), wired to `handleOAuthLogin`.
     - Passkey: full-width passkey button + "Last used" badge, wired to
       `handlePasskeyLogin`.
   - De-dupe: filter the promoted provider out of `providers.map(...)`; hide the bottom
     passkey button when passkey is promoted.
   - Password case: no top slot. Show the "Last used" badge next to the "Sign in"
     button (`data-testid="login-last-used-badge"`) and autofocus the email field.
   - Record the choice: `setLastAuthMethod("oauth:<type>")` in `handleOAuthLogin` before
     redirect; `setLastAuthMethod("password")` in `handleSubmit` after `login()` resolves;
     `setLastAuthMethod("passkey")` in `handlePasskeyLogin` and the conditional-UI success
     path after a successful ceremony.

3. **i18n** — add `"lastUsed"` to the `auth` namespace in all four locales:
   `en` "Last used", `de` "Zuletzt verwendet", `es` "Usado por última vez",
   `fr` "Utilisé en dernier".

4. **Design reference** `web/dash0/src/routes/orgs/$org/design-reference.tsx`
   - Add a "Button with 'Last used' badge" example to the Buttons & badges section,
     showing the promoted-slot pattern (button carrying an inline secondary Badge) with
     its import line.

5. **Playwright E2E** `web/dash0/e2e/login.spec.ts` (extend existing patterns)
   - Deterministic: password-rendering (seed `password`, assert badge visible + email
     focused), no-memory (cleared storage → no slot/badge), recording (log in as test
     user → `localStorage` is `password`).
   - Config-dependent: OAuth promotion + passkey promotion, guarded to skip gracefully
     when the test backend lacks the provider/passkey capability.
