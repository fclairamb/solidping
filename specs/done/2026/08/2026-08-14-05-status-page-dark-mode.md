---
model: sonnet
effort: medium
---

# The public status page has no dark/light mode, unlike the rest of the site

## Problem

The public status page (status0) is permanently light. The groundwork already
exists but is dead code:

- `web/status0/src/index.css:4` declares the Tailwind `dark` custom variant
  and `web/status0/src/index.css:89-124` defines a complete `.dark` token set
  (background, card, brand, status colors…) — but nothing in status0 ever
  applies the `.dark` class to `<html>`, so the dark palette is unreachable.
- dash0 already solved this: `ThemeToggle`
  (`web/dash0/src/components/ui/theme-toggle.tsx`) owns the theme state,
  mirrors it onto `html.dark`, persists to `localStorage("theme")`, and
  defaults to `prefers-color-scheme`.
- The embed widget even supports it already (`data-theme="light"|"dark"|"auto"`
  in `web/status0/src/embed/widget.ts:20`), making the full page the only
  surface without a dark mode.

Subscribers on dark systems get a bright white page, inconsistent with dash0
and the embed widget.

## Proposal

Bring status0 to parity with dash0's theming:

1. **No-flash default**: add a tiny inline script in
   `web/status0/index.html` (before the bundle) that reads
   `localStorage("theme")` and falls back to
   `matchMedia("(prefers-color-scheme: dark)")`, and sets the `dark` class on
   `<html>` before first paint. dash0 applies the class in a `useEffect`,
   which is tolerable behind a login, but a public page must not flash white
   for dark-mode visitors.
2. **Toggle**: port dash0's `ThemeToggle` (Sun/Moon button, same
   `localStorage` mechanism) into status0 and place it in the status page
   header (`web/status0/src/components/shared/status-page-view.tsx`), next to
   the existing header controls. Keep the `data-testid="theme-toggle"` and
   i18n labels (`switchToDarkMode` / `switchToLightMode` — add the keys to
   status0's locales, all languages in `web/status0/src/locales/`).
3. **Also default to system preference** when no stored choice exists, like
   dash0 — three effective states: stored light, stored dark, follow-system.
4. **Sweep for hardcoded colors** that ignore the tokens: the response-time
   chart hardcodes hex values (`#3b82f6`, `#ef4444`, `#facc15`, `#f97316` in
   `web/status0/src/components/shared/response-time-chart.tsx:33-48,125-127`) —
   verify they stay legible on the dark background or switch them to the
   `--status-*` / chart tokens; likewise `<meta name="theme-color">` in
   `web/status0/index.html:14` (consider two media-gated theme-color metas).
5. **Operator custom CSS**: `customCss` is injected after the tokens
   (status-page-view.tsx:249). Dark mode changes what operator overrides see —
   document in `web/docs/docs/features/status-pages.md` (the "Element hooks"
   section) that pages now have a `.dark` ancestor class operators can target,
   and that token-based overrides automatically apply to both modes.
6. **Tests**: a status0 E2E (or extend the existing status-page E2E in
   `web/dash0/e2e/` if that's where public-page coverage lives) asserting the
   toggle flips `html.dark`, persists across reload, and that
   `prefers-color-scheme: dark` yields a dark first paint with no stored
   preference (Playwright `colorScheme: "dark"`).

Out of scope: a per-status-page operator setting to force/disable dark mode —
not requested; the visitor-side toggle matches "like the rest of the website".
