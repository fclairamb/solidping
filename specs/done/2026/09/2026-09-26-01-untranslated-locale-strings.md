---
effort: medium
---

# The French login page says "Sign in with passkey": hundreds of dash0 locale values are still English

## Problem

On the org login page with the UI in French, the passkey button reads "Sign in with
passkey". The key is translated in structure but not in content:

- `web/dash0/src/routes/orgs/$org/login.tsx:782` and `:893` render
  `t("twoFactor.signInWithPasskey")`.
- `web/dash0/src/locales/fr/auth.json:131` holds `"signInWithPasskey": "Sign in with passkey"`,
  the English text copied verbatim. `de/auth.json:131` and `es/auth.json:131` are the same.
- The whole `twoFactor` block (`fr/auth.json:122-132`) is English in fr, de and es:
  `codePrompt`, `recoveryPrompt`, `codeLabel`, `recoveryLabel`, `verify`, `back`,
  `useRecovery`, `useCode`, `signInWithPasskey`. So the 2FA step of the login is English
  for every non-English user.

Neither existing guard can catch this:

- `web/dash0/src/locales/locale-parity.test.ts` checks that every locale has the same
  **keys** as `en`. A key whose value is the English string passes.
- `web/dash0/src/locales/translated-defaults.test.ts` checks that every
  `t("key", "default")` call resolves to a real `en` key. It says nothing about the
  other locales' values.

### How big it is

A quick scan (leaf values identical to `en`, longer than 3 characters) finds about
329 in `fr`, 356 in `de` and 230 in `es`, across dash0's 23 namespaces. The largest:

| Namespace | fr | de | es |
|---|---|---|---|
| `checks.json` | 74 | 81 | 45 |
| `server.json` | 53 | 63 | 51 |
| `account.json` | 48 | 47 | 44 |
| `common.json` | 22 | 20 | 16 |
| `integrations.json` | 20 | 27 | 22 |
| `org.json` | 20 | 24 | 10 |
| `statusPages.json` | 16 | 18 | 9 |
| `incidents.json` | 15 | 9 | 7 |
| `nav.json` | 13 | 8 | 2 |
| `auth.json` | 10 | 11 | 10 |

Some of these are correct as-is: brand and protocol names (`GitHub`, `OAuth`, `SAML`,
`TOTP`, `LDAP`), sample values (`auth.json` `noOrg.joinSlugPlaceholder` = `acme`), and
words that happen to be spelled the same (`Expiration` in French). Most are not:
`account.json` `security.passkeys.*` and `security.totp.*` (the whole passkey and 2FA
settings screens) are English in all three locales.

status0 is essentially clean (1 identical value per locale in `web/status0/src/locales/*/status.json`,
102 keys).

Separately from locale files, strings hardcoded in JSX without `t()` never get translated
at all. They are not covered by either test and are not counted above.

## Proposal

1. **Fix the reported case first**: translate the `twoFactor` block in
   `fr/auth.json`, `de/auth.json`, `es/auth.json`.
2. **Translate every other untranslated value** in dash0's `fr`, `de` and `es` bundles,
   namespace by namespace, starting with the user-facing auth/account/nav/common flows.
   Keep the tone and terminology already used in each locale (e.g. French uses
   "clé d'accès" for passkey in `fr/auth.json:63-64`, so reuse it). Keep interpolation
   placeholders (`{{count}}`, `{{name}}`, …) and plural suffixes intact.
3. **Add a guard** so this does not regrow: a vitest next to `locale-parity.test.ts`
   that fails when a non-`en` leaf value is identical to the `en` value, with an explicit
   allowlist file (key path per locale) for legitimate identicals (brand names, protocol
   acronyms, sample values, same-spelling words). The allowlist must be short and
   reviewed, never a blanket namespace exclusion. Include a positive control (a fixture
   where an identical value is detected) so the test proves it can fail.
4. **Hardcoded JSX strings**: sweep `web/dash0/src` (excluding
   `routes/orgs/$org/design-reference.tsx`, which renders code samples) for user-visible
   English literals outside `t()` (button labels, headings, placeholders, toasts, `aria-label`,
   `title`). Move them to the right namespace with keys in all four locales. If a lint
   rule (e.g. `eslint-plugin-i18next` `no-literal-string` in a scoped mode) is cheap to
   add without flooding the base, add it; otherwise document the sweep method in
   `web/dash0/CLAUDE.md`.
5. Apply the same identical-value check to status0 (`web/status0/src/locales`) and fix
   its one remaining value.
6. Verify in the browser: switch to French and walk login (password, 2FA, passkey
   button), account security, and the check list/detail pages. Extend an existing
   Playwright locale test (or add one) asserting the French login page shows the
   translated passkey button text.

## Open questions

- Should the identical-value guard (step 3) run in CI as a hard failure right away, or
  land as a report first? Recommended: hard failure, with the allowlist, since the
  backlog is cleared in the same change.
- Are machine-quality translations acceptable for `de` and `es`, or should those locales
  be limited to the auth/account flows and flagged for a native review? Recommended:
  translate everything, keep terminology consistent with existing strings, and list any
  uncertain domain terms in the PR description.

## Resolved open questions

- **Should the identical-value guard fail CI right away?** Yes. Make it a hard vitest failure
  from the start, backed by a short, reviewed allowlist of legitimate identicals (key path per
  locale). Clear the whole backlog in the same change so CI stays green.
- **Are machine-quality translations acceptable for `de` and `es`?** Yes. Translate every
  untranslated value in `fr`, `de` and `es`, reuse the terminology already present in each
  locale, and list any uncertain domain terms in the final report (they go in the PR
  description). Never park a translatable string on the allowlist to skip translating it.
