# Passkey login: render a precise error on RP-ID / domain mismatch

## Context
On `http://localhost:4000/dash0/orgs/default/login`, clicking **Sign in with
passkey** fails and the page shows the generic banner *"An unexpected error
occurred. Please try again."* — which tells the user nothing.

Root cause is a WebAuthn **RP-ID / domain mismatch**. The backend derives the
WebAuthn RP ID from `server.base_url`, which is `https://solidping.k8xp.com` in
`server/config.local.yml` (intentionally — OAuth and webhook callbacks need that
public host). When the dashboard is opened at `localhost:4000`, the browser rejects
the ceremony because the RP ID (`solidping.k8xp.com`) is not valid for the
`localhost` domain.

In the login page (`web/dash0/src/routes/orgs/$org/login.tsx`),
`startAuthentication()` from `@simplewebauthn/browser` (v13.3.0) surfaces this as a
`WebAuthnError` with `code === "ERROR_INVALID_RP_ID"` (message: `The RP ID
"solidping.k8xp.com" is invalid for this domain`; the original `SecurityError` is on
`err.cause`). The current `catch` only special-cases `err.name === "NotAllowedError"`
(user cancel) and routes everything else to `reportError`, which — since this is not
an `ApiError` — falls back to `tc("unexpectedError")`, the generic banner.

This spec replaces that generic banner with a clear, passkey-specific message for the
domain mismatch (and a passkey-specific fallback for other WebAuthn failures). It is a
**frontend-only** change: detect the error precisely and render better copy. It does
**not** change the dev RP-ID configuration.

## Goal
When passkey login fails because of an RP-ID / domain mismatch, the user sees a clear,
actionable message (e.g. *"Passkeys aren't available on this domain. Try another
sign-in method."*) instead of the generic "unexpected error" banner. Other passkey
failures get a passkey-specific fallback; user-cancel stays silent.

## Behaviour
The error continues to render in the existing destructive banner —
`<Alert variant="destructive" data-testid="login-error">` (login.tsx ~line 459). Only
the **message text** passed to `setError(...)` changes. Mapping of the error caught in
`handlePasskeyLogin`:

| Error caught | Detection | Result |
|---|---|---|
| User cancelled / timed out | `err instanceof Error && err.name === "NotAllowedError"` | Silent — `return` (unchanged) |
| RP-ID / domain mismatch | `err instanceof WebAuthnError && (err.code === "ERROR_INVALID_RP_ID" \|\| err.code === "ERROR_INVALID_DOMAIN")` | `setError(t("passkeyDomainMismatch"))` |
| Any other WebAuthn failure | `err instanceof WebAuthnError` | `setError(t("passkeyFailed"))` |
| Anything else (network / `ApiError` from begin/finish, etc.) | fallthrough | `reportError(err)` — `ApiError.message` or `tc("unexpectedError")` (unchanged) |

Honest limitation: some browsers report an RP-ID mismatch as `NotAllowedError`
(the library passes it through with that `name`), which is indistinguishable from a
real user-cancel and stays silent. The reported case is Chrome on `localhost`, which
throws `SecurityError` → `ERROR_INVALID_RP_ID`, so it is reliably caught by the
rule above.

## Implementation outline

**New helper** `web/dash0/src/lib/passkey-error.ts` — a pure, i18n-free classifier
(mirrors the `lib/last-auth-method.ts` co-location convention from this branch):

```ts
import { WebAuthnError } from "@simplewebauthn/browser";

export type PasskeyErrorKind = "cancelled" | "domainMismatch" | "failed" | "other";

export function classifyPasskeyError(err: unknown): PasskeyErrorKind {
  if (err instanceof Error && err.name === "NotAllowedError") return "cancelled";
  if (err instanceof WebAuthnError) {
    if (err.code === "ERROR_INVALID_RP_ID" || err.code === "ERROR_INVALID_DOMAIN") {
      return "domainMismatch";
    }
    return "failed";
  }
  return "other";
}
```

`WebAuthnError` and `WebAuthnErrorCode` are exported from `@simplewebauthn/browser`'s
main entry — add to the existing import block in login.tsx (lines 25-29).

**Login page** `web/dash0/src/routes/orgs/$org/login.tsx`
- Add `WebAuthnError` to the existing `@simplewebauthn/browser` import; import
  `classifyPasskeyError` from `@/lib/passkey-error`.
- Rewrite the `handlePasskeyLogin` catch block (lines 328-336):
  ```ts
  } catch (err) {
    switch (classifyPasskeyError(err)) {
      case "cancelled": return;                              // silent (unchanged)
      case "domainMismatch": setError(t("passkeyDomainMismatch")); return;
      case "failed": setError(t("passkeyFailed")); return;
      default: reportError(err);                             // ApiError / generic
    }
  } finally {
    setIsLoading(false);
  }
  ```
  (`t` is the `auth`-namespace translator already bound at login.tsx line 169.)
- Conditional-UI autofill effect (lines 345-376, best-effort): skip the
  `console.warn` when `classifyPasskeyError(err) === "domainMismatch"` so a
  misconfigured domain doesn't spam the console on every page load.

**i18n** — add two flat keys to the **`auth`** namespace in all four locales
`web/dash0/src/locales/{en,de,es,fr}/auth.json`, following the `lastUsed`/`continueWith`
add-pattern from commit `5e130dd8` (flat top-level keys, all four locales together):

- `passkeyDomainMismatch`
  - en: `Passkeys aren't available on this domain. Try another sign-in method.`
  - de: `Passkeys sind auf dieser Domain nicht verfügbar. Bitte eine andere Anmeldemethode verwenden.`
  - es: `Las llaves de acceso no están disponibles en este dominio. Prueba con otro método de inicio de sesión.`
  - fr: `Les clés d'accès ne sont pas disponibles sur ce domaine. Essayez une autre méthode de connexion.`
- `passkeyFailed`
  - en: `Passkey sign-in failed. Please try again or use another method.`
  - de: `Passkey-Anmeldung fehlgeschlagen. Bitte erneut versuchen oder eine andere Methode verwenden.`
  - es: `No se pudo iniciar sesión con la llave de acceso. Inténtalo de nuevo o usa otro método.`
  - fr: `Échec de la connexion par clé d'accès. Réessayez ou utilisez une autre méthode.`

**Design reference** — no change. The error already uses the destructive `Alert`
primitive catalogued in
`web/dash0/src/routes/orgs/$org/design-reference.tsx`. No new visual pattern is
introduced — only copy.

## Out of scope
- **No backend / config change.** `server.base_url` stays `https://solidping.k8xp.com`
  (OAuth/webhook callbacks depend on it); `webauthn.rp_id` is not overridden. Making
  passkeys actually *work* on localhost (RP-ID override or using the tunnel domain) is
  a separate operational choice.
- No change to the passkey begin/finish API, the conditional-UI behaviour (beyond the
  console.warn tweak), or the success path.
- No new error banner component — reuse the existing `login-error` Alert.

## Testing
dash0 has no unit-test runner — only Playwright E2E in `web/dash0/e2e/`. Extend
`e2e/login.spec.ts`, reusing: `fetchAuthCapabilities(baseURL)` helper and the
`test.skip(!passkeysEnabled, "passkeys are not enabled on the test backend")` guard
(login.spec.ts ~lines 187, 320). Test IDs: `passkey-login-button` (button),
`login-error` (the Alert).

**Domain-mismatch message (guarded E2E):**
- Skip if `!passkeysEnabled`.
- `page.route("**/api/v1/auth/passkeys/login/begin", ...)`: await the real response,
  then fulfill with the body's WebAuthn options `rpId` rewritten to a non-matching
  domain (e.g. `example.com`). The browser throws `SecurityError` during
  `navigator.credentials.get()` → `@simplewebauthn/browser` maps it to
  `ERROR_INVALID_RP_ID`. No virtual authenticator is needed (the RP-ID check precedes
  authenticator interaction).
- Navigate to `orgs/test/login`, click `passkey-login-button`, assert
  `getByTestId("login-error")` is visible and contains the `passkeyDomainMismatch`
  copy — explicitly **not** the generic `common.unexpectedError` text.

**Manual verification (definitive, reproduces the real bug):**
- `make dev-test`, open `http://localhost:4000/dash0/orgs/default/login`, click
  **Sign in with passkey**, confirm the banner now shows the domain-mismatch message
  instead of "An unexpected error occurred."
- Confirm no missing-key warnings in the four locale files.

## Implementation Plan
1. **Helper** `web/dash0/src/lib/passkey-error.ts` — `classifyPasskeyError(err)`
   returning `"cancelled" | "domainMismatch" | "failed" | "other"`, importing
   `WebAuthnError` from `@simplewebauthn/browser`. Pure, no i18n.
2. **login.tsx** — add `WebAuthnError` + helper imports; switch `handlePasskeyLogin`
   catch on `classifyPasskeyError`; silence conditional-UI `console.warn` for
   `domainMismatch`.
3. **i18n** — add `passkeyDomainMismatch` and `passkeyFailed` to
   `web/dash0/src/locales/{en,de,es,fr}/auth.json` (copy above).
4. **E2E** `web/dash0/e2e/login.spec.ts` — guarded domain-mismatch test that rewrites
   the begin response's `rpId` and asserts the specific `login-error` copy.
5. Verify: `make lint` (dash), run new Playwright test, manual localhost check.
