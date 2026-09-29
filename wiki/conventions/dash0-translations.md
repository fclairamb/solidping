# dash0 translations (en, fr, de, es)

Read when adding or changing any user-visible dash0 string.
Every user-visible string goes through `t()` and exists in all four
`src/locales/<lang>/<namespace>.json` files. Three unit tests and one lint
rule guard this (spec 2026-09-26-01 for the last two):

- `locale-parity.test.ts`: every locale has exactly `en`'s keys.
- `translated-defaults.test.ts`: every `t("key", "default")` resolves to a real
  `en` key.
- `untranslated-values.test.ts`: a fr/de/es value identical to the `en` value
  fails, unless `src/locales/identical-allowlist.ts` names that key for that
  locale. It also requires each translation to keep `en`'s `{{placeholders}}`
  and Trans `<tags>`, fails on stale allowlist entries, and checks
  `web/status0/src/locales` too (status0 has no unit-test runner). Add an
  allowlist entry only for a value that is right as-is (brand, protocol, sample
  value, a word spelled the same); translate everything else.
- `i18n-local/no-untranslated-jsx-text` (`eslint-rules/`, RuleTester controls
  next to it, run by `bun run test:unit`): JSX text children, string literal
  children, ternary branches and template-literal text, and the same forms in
  `aria-label` / `title` / `alt` / `label`, must go through `t()`. The rule's
  `allow` list in `eslint.config.js` holds whole-word brand, protocol, unit and
  command phrases only; text made of nothing else passes.
- Validators outside React (`components/checks/form/types/*.tsx` `toConfig`)
  use `validationMessage(key)` from `form/types/common.ts`, which reads
  `checks:validation.<key>` from the shared `@/i18n` instance.

The rule does not look at `placeholder` (mostly sample values like
`example.com`) or at strings built outside JSX (toast messages, summary lines in
helpers). When touching a form, sweep those by hand: list the string-literal
`placeholder=` attributes and `toast*("…")` calls in the files you changed
(`grep -nE 'placeholder="[^"]*[a-z]{2}|toast(\.[a-z]+)?\("' <files>`) and move
anything a user reads as prose into the locale files.

Terminology already chosen per locale, reuse it: fr "clé d'accès" (passkey),
"contrôle" (check), "astreinte" (on-call), vous; de "Passkey", "Prüfung",
"Bereitschaft", Sie; es "llave de acceso", "comprobación", "guardia", tú.
