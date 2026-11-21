---
model: haiku
effort: low
---

# A `?lang=` value that is not a valid language tag blanks the public status page

## Problem

Found by the security scan of 2026-11-20 (finding AS2). Severity: low. Who can trigger it: anyone. They send a status page link with a crafted `?lang=` value, and every visitor who opens that link sees a blank page.

- status0 detects the language from `?lang=`, then `navigator` (`web/status0/src/i18n.ts:22-25`), with no `supportedLngs`. i18next keeps any detected value it can't match as `i18n.language`. With `123`, `en_` or `a-b-c`, `i18n.language` is that string and `resolvedLanguage` is `en`. Checked with the i18next version in the lockfile.
- That raw value reaches `Intl` during render:
  - `new Intl.RelativeTimeFormat(lang)` in `status-update-card.tsx:60-76` (called at `:151`);
  - `toLocaleDateString` / `toLocaleString` in `availability-bar.tsx:98,108`;
  - `response-time-chart.tsx:78,82,308,335,428,450,454,510`;
  - `formatLastChecked` in `status-page-view.tsx:264`.
- `Intl` throws `RangeError: Invalid language tag` for these values. status0 has no error boundary, so React unmounts the whole page.
- `useLanguageFromPage` (`hooks/useLanguageFromPage.ts:16`) deliberately leaves the language alone when `?lang=` is present, so the page's configured language doesn't save it.
- Not persistent: the same page without the parameter works. But a status page is what people open during an outage, and the link can be posted wherever the provider's users talk.
- dash0 isn't affected: its `?lang=` goes through `supportedLanguage()` (`web/dash0/src/lib/languages.ts:19`).

## Proposal

1. In `web/status0/src/i18n.ts`, set `supportedLngs: ["en", "fr", "de", "es"]` and `nonExplicitSupportedLngs: true`, so `fr-CA` still resolves to `fr` and anything else falls back to `en`.
2. Pass one helper to every `Intl` / `toLocale*` call instead of `i18n.language`: `i18n.resolvedLanguage || "en"`, or a `displayLocale()` that returns a tag `Intl.getCanonicalLocales` accepts. Use it at the call sites listed above.
3. Add a top-level error boundary in `web/status0/src/app.tsx` that renders a minimal "status page unavailable, reload" message instead of a blank page.

## Tests

- `web/status0/src/lib/*.test.ts`: the helper returns a tag `Intl` accepts for `123`, `en_`, `a-b-c` and an empty string, and returns `fr` for `fr` and `fr-CA`. The `123` case fails before the fix.
- A status0 Playwright spec (`web/status0/e2e/`): opening a status page with `?lang=123` shows the page name and its checks. It fails before the fix.
- Positive control in the same spec: `?lang=fr` renders the French labels.
