# Login page layout polish: inline forgot-password, tighter dividers, passkey as link

## Context
The login page (`web/dash0/src/routes/orgs/$org/login.tsx`) currently stacks, top
to bottom: up to five OAuth provider buttons, an OR divider, the email field, the
password field, the Sign in button, a full-width "Sign in with passkey" outline
button, and a centered "Forgot password?" link. The page works, but it is taller
and busier than it needs to be: three full-height action rows below the form
fields, generous divider padding, and a forgot-password link visually detached
from the password field it relates to.

We explicitly considered and **rejected** collapsing email/password behind a
"Continue with email" method button: visible fields keep password-manager
autofill instant and anchor the WebAuthn conditional UI
(`autoComplete="username webauthn"` on the email input, login.tsx ~line 665).
The structure stays; this spec only polishes it.

## Goal
Reduce the vertical footprint and visual noise of the login card without
removing any capability:

1. **"Forgot password?" moves inline with the Password label** — right-aligned
   on the same row, where the eye expects it.
2. **OR divider zones get tighter** — less vertical air between the OAuth grid
   and the email form.
3. **"Sign in with passkey" becomes a text link** instead of a full-height
   outline button (the *promoted* last-used passkey button is unaffected).

## Behaviour

### 1. Forgot password inline with the Password label
- The password field block (login.tsx lines 677-688) gets a label row:
  ```tsx
  <div className="flex items-center justify-between">
    <Label htmlFor="password">{tc("password")}</Label>
    <Link
      to="/forgot-password"
      search={{ email: email || undefined }}
      className="text-sm text-muted-foreground hover:underline"
    >
      {t("forgotPassword")}
    </Link>
  </div>
  ```
- The existing centered forgot-password block at the bottom of the form
  (lines 731-739) is removed. The `search={{ email }}` prefill behaviour is
  preserved verbatim.

### 2. Tighter divider spacing
- Both OR dividers (the one after the promoted last-used block, lines 611-620,
  and the one after the OAuth grid, lines 645-654) change `my-4` → `my-3`.
- The wrapping `mb-4` on the promoted block (line 567) and the OAuth grid block
  (line 625) change to `mb-3`.
- The divider markup itself (border-t + centered `{tc("or")}` chip) is
  unchanged — it matches the pattern in the design reference.

### 3. Passkey sign-in as a text link
- The non-promoted passkey button (lines 716-730, `variant="outline"`,
  full-width) becomes a centered text link below the Sign in button:
  ```tsx
  {passkeysEnabled && browserSupportsWebAuthn() && !promotePasskey && (
    <div className="text-center">
      <button
        type="button"
        onClick={handlePasskeyLogin}
        disabled={isLoading}
        className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:underline disabled:opacity-50"
        data-testid="passkey-login-button"
      >
        <KeyRound className="h-3.5 w-3.5" />
        {t("twoFactor.signInWithPasskey")}
      </button>
    </div>
  )}
  ```
- It keeps `data-testid="passkey-login-button"` so existing E2E assertions
  (e.g. `e2e/login.spec.ts:340` asserts it has count 0 when promoted) keep
  working unchanged.
- The **promoted** passkey button (`passkey-login-button-promoted`, lines
  592-609) stays a full outline button with the "Last used" badge — when
  passkey is the user's method of choice it deserves the prominent slot.
- Same conditions as today: rendered only when `passkeysEnabled`,
  `browserSupportsWebAuthn()`, and not already promoted.

### Resulting layout (default case, no last-used)
OAuth grid → OR divider (tight) → Email → Password (label + forgot link on one
row) → Sign in → "Sign in with passkey" link → registration hint. Two fewer
full-height rows than today.

## Out of scope
- **No structural change to sign-in methods.** Email/password stays a visible
  inline form; explicitly not collapsed behind a method button.
- No change to the promoted last-used block, OAuth handlers, passkey logic, 2FA
  step, registration link, or any backend/i18n surface (all strings —
  `forgotPassword`, `twoFactor.signInWithPasskey`, `or` — already exist in all
  locales).
- No conditional "Last used" badge suppression (proposal 4 from the discussion
  was dropped).

## Testing
dash0 has Playwright E2E only (`web/dash0/e2e/`).

- **Existing tests must stay green**: `e2e/login.spec.ts` already asserts
  `passkey-login-button` is absent when promoted (line 340) and uses
  `passkey-login-button-promoted` (line 338); test IDs are preserved.
- **New/updated assertions** in `e2e/login.spec.ts`:
  - The forgot-password link is visible on the password label row and navigates
    to `/forgot-password`, carrying a typed email as the `email` search param.
  - The passkey control (`passkey-login-button`) is still clickable and triggers
    the passkey flow (reuse the existing `passkeysEnabled` guard pattern).
- **Manual / visual**: `make dev-test`, open
  `http://localhost:4000/dash0/orgs/test/login`, verify desktop + mobile
  viewport (link touch targets remain comfortable), light and dark mode.

## Implementation Plan
1. **login.tsx — forgot password**: wrap the Password `Label` in a
   `flex items-center justify-between` row containing the existing `Link`;
   delete the bottom centered block.
2. **login.tsx — dividers**: `my-4` → `my-3` on both divider wrappers; `mb-4` →
   `mb-3` on the promoted and grid containers.
3. **login.tsx — passkey link**: replace the outline `Button` with the centered
   text-link button above, keeping the `data-testid` and render conditions.
4. **E2E**: update/add `login.spec.ts` assertions for the relocated
   forgot-password link and the passkey link.
5. Verify: `bun run lint` (dash0), `make test-dash`, manual check on mobile
   viewport and dark mode.
