---
model: opus
effort: high
---

# Public status pages keep the old palette: align status0 with the electric identity without breaking customer theming

Spec 4 of the "electric identity" series. It takes its palette from
[spec 01](2026-09-24-01-dash0-electric-identity-tokens-and-primitives.md)
§1 and §2. It does not depend on any dash0 code change (there is no shared
package between the two apps), so it can land in any order.

## Problem

status0 duplicates dash0's tokens in
[web/status0/src/index.css](web/status0/src/index.css). Light is at
l.65-131 and dark at l.134-180, with the same `--primary` (a mid blue) and
`--brand` (crimson) as dash0 today. Once spec 01 lands, the dashboard and
the public page of the same product will speak two different palettes:

- A customer edits their page in dash0 (appearance editor, live preview) and
  sees the electric UI around an old-palette page.
- `theme-color` is crimson (`#e91e63` light, `#0e0a0e` dark) in
  [web/status0/index.html](web/status0/index.html) and in
  `public/manifest.webmanifest`.
- Some neutrals are hardcoded outside the tokens:
  - the TV shell `bg-[oklch(0.19_0.01_250)]`
    ([tv-route.tsx:61](web/status0/src/components/tv/tv-route.tsx:61))
  - the embeddable widget's own hex palette, including a `#2563eb` maintenance
    dot ([embed/widget.ts:102-168](web/status0/src/embed/widget.ts:102))

A status page belongs to the **customer**, not to SolidPing. The identity has
to show up as quality and polish, not as SolidPing branding painted over
someone else's page.

## Constraints (hard)

- **The public theming API does not change.** Customer CSS is injected after
  `index.css`
  ([status-page-view.tsx:404](web/status0/src/components/shared/status-page-view.tsx:404)),
  and it is documented in
  [web/docs/docs/features/status-pages.md](web/docs/docs/features/status-pages.md)
  (~l.573-710). Every documented variable keeps its name and meaning, and every
  `sp-*` hook stays. An existing custom stylesheet must render exactly as it
  did.
- `--brand` stays crimson by default. It is the logo tint and the "Powered by
  SolidPing" link, which matches the "the logo stays crimson" decision.
- Status colors (`--status-ok/warning/error`) are unchanged.
- `hideBranding` and white-label behaviour are unchanged.

## Proposal

### 1. Tokens

- Port spec 01 §1 (neutrals, `--primary`, `--ring`, `--chart-1`,
  `--chart-5`) into `web/status0/src/index.css` for both themes, with the same
  values.
- Port spec 01 §2's `--primary-gradient`, `--gradient-foreground` and the
  `bg-primary-gradient` utility. Skip the page glow, the hero gradient and
  the accent gradient. A customer's page gets no decorative SolidPing glow.
- Add a comment at the top of both token blocks: "values mirror
  web/dash0/src/index.css, keep them in sync". Keep them duplicated, because
  a shared package is out of scope.

### 2. Where the identity shows

- **Buttons.** The two solid primary buttons, subscribe
  ([subscribe-widget.tsx:84](web/status0/src/components/shared/subscribe-widget.tsx:84))
  and unlock
  ([unlock-form.tsx:97](web/status0/src/components/shared/unlock-form.tsx:97)),
  use `bg-primary-gradient` with the same inner highlight and tinted shadow as
  the dash0 button. Keep `bg-primary` underneath as the fallback color.
- **Links, the maintenance state and the response-time chart** follow
  `--primary`. There is no change in the components.
- **Nothing else changes.** The header brand bar, cards, uptime bars and
  incident history stay flat.

### 3. Customers can re-theme the new pieces

Today a customer who sets `--brand` to their color still gets SolidPing-blue
buttons. That was true before this spec too. With a gradient it becomes more
visible, so make it overridable:

- Add `--primary`, `--primary-foreground` and `--primary-gradient` to the
  documented variable table in `status-pages.md`. `--primary` is "buttons,
  links, maintenance state". `--primary-gradient` is "button fill; set it to
  your color (a flat `linear-gradient(<c>, <c>)` works) to drop the gradient".
- Add the same three lines, commented out, to the appearance editor's
  `STARTER_TEMPLATE`
  ([status-pages.$statusPageUid.appearance.tsx:37](web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.appearance.tsx:37)).
  Its comment requires it to stay in sync with the docs page.
- The starter template's default neutrals (`--background: #f8fafc`,
  `--foreground: #0f172a`, `--border: #e2e8f0`, ...) become the new values:
  `#f5f9fc`, `#09121f`, `#dee3eb`, and so on (spec 01 §1 hex column).
  `--brand: #e11d63` stays.

### 4. Hardcoded colors

- **Browser chrome**: set `theme-color` and the manifest `theme_color` to
  the page background (`#f5f9fc` light, `#060a13` dark), not navy. The browser
  bar should blend into the customer's page, not into SolidPing's sidebar.
- **TV shell** (`tv-route.tsx:61`): the neutral `oklch(0.19 0.01 250)`
  becomes the dark `--background` / `--card` pair from spec 01. Leave the
  status-tinted `tv*` surfaces in
  [status-style.ts:85-200](web/status0/src/lib/status-style.ts:85) alone.
  They encode status.
- **Embed widget**: map its neutrals to the new hex values and its `#2563eb`
  maintenance dot to `#1e64ef`. Its status colors are unchanged.

## Out of scope

- A shared token package between dash0 and status0.
- The OG image (`public/og-default.png`), the favicon and the logo.
- Status colors, and the TV board's status-tinted surfaces.
- The "Powered by" footer copy and behaviour.

## Acceptance criteria

- [ ] status0's neutrals, `--primary`, `--ring`, `--chart-1` and `--chart-5`
      match spec 01 in both themes.
- [ ] Subscribe and unlock buttons use the gradient, and nothing else on the
      page does.
- [ ] A status page with a custom stylesheet that overrides every documented
      variable renders identically before and after.
      Prove it with a test that injects such a stylesheet.
- [ ] `--primary`, `--primary-foreground` and `--primary-gradient` are
      documented in `status-pages.md` and in the editor's starter template.
      A stylesheet that sets them re-themes the buttons.
- [ ] `theme-color` / manifest, the TV shell and the embed widget carry no
      old-palette values.
- [ ] `hideBranding` pages still hide the SolidPing mark and the "Powered by"
      link.

## Verification

- status0 `bun run lint` and its unit tests.
- dash0 e2e `status-page-appearance.spec.ts` (live preview) and
  `status-page-custom-domain.spec.ts`.
- Screenshots of a status page (all operational, one degraded, one in
  maintenance) in both themes, with and without a custom stylesheet, in the
  PR.

## Implementation Plan

1. **Tokens** (`web/status0/src/index.css`). In `:root` and `.dark`, copy
   dash0's shipped values for the neutrals (`--background`, `--foreground`,
   `--card(-foreground)`, `--popover(-foreground)`, `--secondary(-foreground)`,
   `--muted(-foreground)`, `--accent(-foreground)`, `--border`, `--input`),
   `--primary`, `--primary-foreground`, `--ring`, `--chart-1`, `--chart-5`,
   plus `--primary-gradient` and `--gradient-foreground`. `--control` is not
   ported: nothing in status0 reads it. Untouched: `--brand*`, `--status-*`
   (including status0's own `--status-neutral*`), `--destructive`,
   `--chart-2..4`, `--radius`. Both blocks open with "values mirror
   web/dash0/src/index.css, keep them in sync". Add
   `--color-gradient-foreground` and `--inset-shadow-highlight` to
   `@theme inline`, and the `bg-primary-gradient` utility. No page glow, no
   hero or accent gradient.
2. **Customer theming keeps precedence.** Every token stays declared on plain
   `:root` / `.dark` (never `:root.dark`, `html`, a layer or `!important`), so
   the operator `<style>` rendered later in the document still wins on equal
   specificity. The button reads only variables (`--primary-gradient`,
   `--primary`, `--gradient-foreground`, shadows mixed from `--primary`), so an
   operator override re-themes it completely.
3. **Buttons.** Subscribe and unlock: `bg-primary bg-primary-gradient
   text-gradient-foreground inset-shadow-highlight shadow-primary
   hover:brightness-105 hover:shadow-primary-hover
   motion-safe:hover:-translate-y-px`, the same recipe as the dash0 default
   button. Nothing else gets a gradient.
4. **Docs and starter template.** `status-pages.md` variable table gets
   `--primary`, `--primary-foreground`, `--primary-gradient`, plus
   `--gradient-foreground` (the button label: without it an operator with a
   light brand color could not make the label readable, since
   `--primary-foreground` has to stay dark on the dark theme's light-blue
   badge fill), and a note that a stylesheet already setting `--primary`
   should now set `--primary-gradient` too. dash0's `STARTER_TEMPLATE` gets
   the same lines commented out, and its default neutrals move to the new hex
   values (light `#f5f9fc` / `#09121f` / `#dee3eb`, dark `#060a13` /
   `#edf2f9` / `#0c131e` / `#1f293a`). `--brand: #e11d63` stays.
5. **Hardcoded colors.** `theme-color` metas become `#f5f9fc` (light) and
   `#060a13` (dark); manifest `theme_color` and `background_color` become
   `#f5f9fc`. The TV shell uses `bg-background text-foreground` inside its
   `.dark` wrapper (the dark tokens). The embed widget's neutrals move to
   `#ffffff` / `#09121f` / `#dee3eb` (light) and `#0c131e` / `#edf2f9` /
   `#1f293a` (dark), and its maintenance dot to `#1e64ef`. `status-style.ts`
   TV surfaces are left alone.
6. **Tests.**
   - status0 unit (`bun test ./src`): the ported tokens equal dash0's in both
     themes; documented variables are declared only on `:root` / `.dark`;
     theme-color, manifest, widget and TV shell carry no old-palette value;
     docs and starter template list the new variables.
   - status0 e2e `electric-identity.spec.ts`: default tokens reach the page;
     subscribe and unlock paint the gradient and are the only elements that
     do; a stylesheet overriding every previously documented variable
     resolves to the operator's values in light and dark (also run against
     the pre-change build as the "before"); a stylesheet setting the new
     variables re-themes the button; `hideBranding` + logo still shows no
     SolidPing mark and no "Powered by".
   - dash0 e2e `status-page-appearance.spec.ts`: the starter template lists
     the new variables.
