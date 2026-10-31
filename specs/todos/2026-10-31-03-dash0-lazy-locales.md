---
model: sonnet
effort: medium
pr: own
automerge: false
---

# dash0 ships and parses every language's translations on every page load

## Problem

`web/dash0/src/i18n.ts` statically imports every locale JSON for en, fr, de and es (`src/i18n.ts:4-95`) and passes them all to `i18n.init({ resources })` (`src/i18n.ts:100-202`). The main chunk therefore carries all four languages, and every visitor downloads and parses about three languages they never use.

Measured 2026-10-31 on the dash0 build at `8344a9d8` (`vite build`, M-series Mac):

| | Size |
|---|---|
| Main chunk (`assets/index-*.js`) | 4,616,472 B raw, 1,288,936 B gzipped |
| Locale JSON, all four languages | 1,260,845 B (en 288,150, fr 326,857, de 324,012, es 321,826) |

- Compiling the main chunk in V8 (`vm.SourceTextModule`, 40 interleaved rounds, compile cache defeated) takes a median of about **57.5 ms**, about 12.5 ns per byte.
- At that rate, the ~0.95 MB of unused languages costs roughly **12 ms** of compile time per load on this machine. That is several times more on a phone, plus about 0.3 MB of extra download (gzipped).

## Proposal

1. In `src/i18n.ts`, keep English (the `fallbackLng`, `src/i18n.ts:204`) imported eagerly. Load fr, de and es on demand, one chunk per language. Use either a `import.meta.glob`/dynamic `import()` per language or `i18next-resources-to-backend`. Every namespace of a language goes in that language's single chunk, not one chunk per namespace.
2. Before the first render, load the detected language (`detection` order localStorage → navigator, `src/i18n.ts:206-210`). `src/main.tsx:22` imports `./i18n` for its side effect; it must wait for that language before mounting, per the open question below.
3. Switching language must work without a reload at every call site:
   - `src/contexts/AuthContext.tsx:395` (`void i18n.changeLanguage(plan.changeLanguage)`) and `:435`
   - `src/components/shared/language-switcher.tsx:24`
   - `src/routes/orgs/$org/account.profile.tsx:337`

   `changeLanguage` must resolve only once the language's chunk is loaded. A failed chunk load keeps the current language and surfaces through the existing `.catch` / `await` paths.
4. CSP stays as it is: no `eval` / `new Function`, chunks served first-party from the dashboard's own assets (`server/internal/securityheaders/securityheaders.go`), and no new inline script.
5. Record the before/after bundle size and compile time in the PR description, measured the same way as above.

## Tests

- `src/locales/locale-parity.test.ts`, `translated-defaults.test.ts`, `untranslated-values.test.ts` stay green and unchanged. They read the JSON files from disk, not through `i18n.ts`.
- New vitest unit test for the loader:
  - `changeLanguage("fr")` resolves with French strings available.
  - An unknown language falls back to English.
  - A loader that rejects leaves the current language active (failure path).
- Playwright, extending `e2e/email-language.spec.ts` / `e2e/login-passkey-i18n.spec.ts` or a new spec:
  - A visitor with `solidping_language=fr` in localStorage sees French on first paint, with no raw keys and no English flash.
  - Switching to `de` through `language-switcher` updates the UI without a reload, and requests a `de` chunk.
  - Negative control: an English visitor's page load requests no fr, de or es chunk.
- Build check: the main chunk is smaller by about the size of three locales (≈ 0.9 MB raw).

## To verify

- react-i18next defaults to `useSuspense: true`, and no `<Suspense>` boundary shows up in `src/main.tsx` or `src/i18n.ts`. Check how components behave while a language is loading. The likely route is `react: { useSuspense: false }` plus awaiting the language before mounting.
- The vitest setup and existing component tests may switch to fr/de/es synchronously after importing `./i18n`. Each such test must now await the language.
- The dashboard `script-src` must allow the lazily imported chunks from the same origin in production (`make dev` proxies to Vite without CSP, so check a built binary). Check that the Vite modulepreload helper adds no inline script outside the hashed SPA shell.

## Open questions

- First paint for a non-English visitor: wait for the language chunk before rendering, or render English and then switch? **Recommended: wait.** The chunk is first-party and small once gzipped. If it fails or takes longer than about 2 s, render English rather than a blank page.
