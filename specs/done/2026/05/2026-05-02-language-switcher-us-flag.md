# Language switcher — replace UK flag with US flag for English

## Context

The language switcher in both frontend apps (`dash0` admin and `status0` public status page) uses the UK flag (🇬🇧) to represent the English locale. The English translations in `web/*/src/locales/en/` use US-style English, and the broader user base for SolidPing skews toward US English defaults. The UK flag is misleading; switch to the US flag (🇺🇸).

## Files to change

The same `LANGUAGES` constant is duplicated in both apps. Update both.

### 1. `web/dash0/src/components/shared/language-switcher.tsx`

Line 10 currently reads:

```ts
{ code: "en", flag: "\u{1F1EC}\u{1F1E7}", label: "English" },
```

Change to:

```ts
{ code: "en", flag: "\u{1F1FA}\u{1F1F8}", label: "English" },
```

(`\u{1F1EC}\u{1F1E7}` = regional indicators G+B = 🇬🇧. Replace with `\u{1F1FA}\u{1F1F8}` = U+S = 🇺🇸.)

### 2. `web/status0/src/components/shared/language-switcher.tsx`

Same change on line 10 — identical `LANGUAGES` array, identical replacement.

## Out of scope

- The `label` stays `"English"` (the language name, not a country).
- The `code` stays `"en"` (i18n key — not a country code).
- French, German, and Spanish flag entries are correct and untouched.
- No changes to translation JSON files — this is purely a flag-emoji swap.
- No changes to `resolveLang`, the language detection logic, or any consumer of `LANGUAGES`.

## Verification

1. `make build-dash0 && make build-status0` — confirms no TypeScript errors.
2. `make dev-test` is already running on port 4000. Open in browser:
   - `http://localhost:4000/dash0/` — open the language switcher dropdown in the org header. The English entry should show 🇺🇸. When English is the active language, the trigger button should also display 🇺🇸.
   - `http://localhost:4000/status0/default/test` — same check on the public status page header.
3. Confirm the other three flags (🇫🇷, 🇩🇪, 🇪🇸) are unchanged.

## Final grep check

After the change, this should return zero matches outside `node_modules/`:

```bash
rtk grep -rn "1F1EC.*1F1E7" --include="*.ts" --include="*.tsx" web/ --exclude-dir=node_modules
```

And this should return exactly two matches (one per app):

```bash
rtk grep -rn "1F1FA.*1F1F8" --include="*.ts" --include="*.tsx" web/ --exclude-dir=node_modules
```

---

## Implementation Plan

1. Replace `\u{1F1EC}\u{1F1E7}` with `\u{1F1FA}\u{1F1F8}` in `web/dash0/src/components/shared/language-switcher.tsx` and `web/status0/src/components/shared/language-switcher.tsx`.
