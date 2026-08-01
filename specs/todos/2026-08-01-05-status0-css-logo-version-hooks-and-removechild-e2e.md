---
model: opus
effort: high
---

# Custom CSS cannot retarget the logo or version, and the recurring status0 "removeChild" crash has no regression guard

## Problem

Two related gaps on the public status page (status0):

1. **The custom CSS feature (spec 2026-07-27-02) cannot override the logo or
   the version/footer.** The header logo is a plain `<img src=".../logo.svg">`
   rendered by `Logo`
   ([logo.tsx:18-33](web/status0/src/components/ui/logo.tsx)) inside
   [status-page-view.tsx:204](web/status0/src/components/shared/status-page-view.tsx),
   and the footer renders a "Powered by SolidPing" link plus
   `v{versionInfo.version}`
   ([status-page-view.tsx:282-292](web/status0/src/components/shared/status-page-view.tsx)).
   None of these elements carry a stable, documented class or attribute, so an
   operator writing custom CSS has no reliable selector to replace the logo
   with their own image or hide/restyle the version line. Tailwind utility
   classes are not a usable API — they change with any styling refactor.

2. **The "Failed to execute 'removeChild' on 'Node'" crash keeps coming back
   on /status0** (surfaced as TanStack Router's default "Something went
   wrong!" error boundary — see attached screenshot from the user report).
   The known root cause is documented in
   [main.tsx:48-64](web/status0/src/main.tsx): Chrome auto-translate wraps
   text nodes in `<font>` tags, which breaks React reconciliation on the next
   re-render. A mitigation (syncing `<html lang>` with the active i18n
   language) was added, but the error still recurs "quite frequently" per the
   user — so either the mitigation is insufficient (e.g. Chrome still offers
   translation on mixed-language pages, or the user manually translates), or
   another DOM-mutating actor (extension, the custom `<style>` text child,
   the language switcher re-render) hits the same failure mode. There is no
   E2E test reproducing the mutation, so regressions ship silently.

## Proposal

### Part 1 — stable CSS hooks for logo, page name, and version/footer

Give the brandable elements stable, documented class names (the `sp-` prefix
as the public theming API, alongside the existing CSS variables):

- In [status-page-view.tsx](web/status0/src/components/shared/status-page-view.tsx):
  - Wrap/annotate the header logo: `className="sp-logo"` on the `Logo`
    element (Logo already accepts and merges `className`,
    [logo.tsx:23](web/status0/src/components/ui/logo.tsx)).
  - `sp-page-name` on the page-name `<span>` (line 205).
  - `sp-footer` on the footer container (line 282), `sp-powered-by` on the
    outbound link (line 283), `sp-version` on the version `<span>`
    (line 291).
- **Logo replacement must work with CSS only.** Verify and document the
  technique: `.sp-logo img { content: url("https://…/my-logo.svg"); }`
  (works in Chromium/Safari), with the fallback pattern
  `.sp-logo img { display: none } .sp-logo { background: url(…) no-repeat
  center / contain; width: 32px; height: 32px; }` for full browser coverage.
  Ensure the markup shape makes the fallback viable (the wrapper must be the
  sized element, not collapse to zero when the img is hidden).
- Hiding the version is then just `.sp-version { display: none }` (same for
  `sp-powered-by` if the operator wants a fully white-label footer — that is
  acceptable: custom CSS could already hide it via structural selectors).
- Update the **starter template** shown in the dash0 appearance editor empty
  state (route `/orgs/$org/status-pages/$statusPageUid/appearance`, covered
  by `web/dash0/e2e/status-page-appearance.spec.ts`) to list the new `sp-*`
  hooks with commented examples (replace logo, hide version).
- Update the docs "Custom CSS" section in
  `web/docs/docs/features/status-pages.md`: add an "Element hooks" table
  (`sp-logo`, `sp-page-name`, `sp-footer`, `sp-powered-by`, `sp-version`)
  and the logo-replacement snippet.
- These class names are API: add a short comment at each usage site noting
  they are documented theming hooks and must not be renamed casually.

No backend/API change — this is selectors + docs only.

### Part 2 — E2E regression guard for the removeChild crash

Add a Playwright spec in `web/status0/e2e/` (sibling of
`status-page.spec.ts`) that reproduces the translate-style DOM mutation and
asserts the page survives:

- Load a status page, then via `page.evaluate` simulate Chrome
  auto-translate: walk visible text nodes and wrap them in `<font
  style="vertical-align: inherit">` elements (this is exactly what Chrome
  translate does).
- Trigger the re-renders that historically crash: switch language through
  the `LanguageSwitcher`, and force a data refetch (the 30 s
  `refetchInterval` — trigger via reload-free refetch, e.g. dispatch
  visibility/focus or wait with a shortened interval).
- Assert the TanStack Router default error boundary never appears
  (`text="Something went wrong!"`), the page content is still present, and
  no uncaught `removeChild` page error was recorded (`page.on("pageerror")`).
- Also assert the existing mitigation holds: `<html lang>` matches the
  selected language after switching (guards
  [main.tsx:52-64](web/status0/src/main.tsx) against regression).

If writing the test reveals that the mitigation is genuinely insufficient
(the mutation still crashes React), fix the root cause rather than loosening
the test — candidate hardening, in order of preference:
1. A route-level `errorComponent`/recovery that remounts the subtree instead
   of showing the raw error UI (last-resort safety net, still fails the
   test's "no pageerror" assertion unless recovery is silent — prefer 2).
2. Structural hardening of the crash-prone render sites (e.g. keyed
   wrappers / `translate="no"` on dynamic status text, as appropriate) so
   reconciliation doesn't try to remove foreign-wrapped nodes.
Keep whatever fix is chosen scoped and documented next to the existing
comment in main.tsx.

### Testing

- New Playwright spec above runs green under the standard status0 E2E setup.
- Extend `web/dash0/e2e/status-page-appearance.spec.ts` (or the new spec)
  with one case: apply custom CSS using `.sp-version { display: none }` and
  a logo override, assert the version line is hidden and the logo swap is
  applied on the public page.
- `make lint` green; no *new* eslint errors (pre-existing dash0 debt stays).

### Out of scope

- Structured branding controls (logo upload field, color pickers) — still a
  possible follow-up, per spec 2026-07-27-02.
- Any backend or API change.
