---
model: opus
effort: high
---

# dash0 chrome is one flat light plane: dark navy sidebar, gradient page header tile, page glow, hero KPI tile

Spec 2 of the "electric identity" series. The palette, gradient tokens and
utilities come from
[spec 01](2026-09-24-01-dash0-electric-identity-tokens-and-primitives.md),
which must land first. The visual target is
[wiki/design/2026-09-24-identity-directions.html?dir=electric](wiki/design/2026-09-24-identity-directions.html).
Open it in the browser pane before you start.

## Problem

Spec 01 recolors the primitives. The *shape* of the app still reads as a stock
template:

- **The sidebar is a near-white column** on a near-white page. There is no
  frame, and the logo floats. The active item is a pale `sidebar-accent`
  wash with no marker ([sidebar.tsx:444](web/dash0/src/components/ui/sidebar.tsx:444)).
- **Every page header looks the same and neutral.** The icon tile in
  [page-header.tsx:31](web/dash0/src/components/shared/page-header.tsx:31)
  is `bg-muted text-foreground`, and the `h1` is `font-semibold`.
- **The top of the page is dead space.** Nothing ties the content area to the
  brand.
- **The dashboard KPI row is four identical white cards.** The headline number
  has no more weight than the others.

Decision (see spec 01): the sidebar is **always dark navy**, in both themes.
Headings stay Inter, but heavier.

## Proposal

As in spec 01, build every piece on the design reference page first
([design-reference.tsx](web/dash0/src/routes/orgs/$org/design-reference.tsx)),
then check it on real routes.

### 1. Always-dark sidebar

**Scope the sidebar subtree as dark.** Several things inside the sidebar read
non-sidebar tokens, and they would break on a dark panel in light mode:

- `text-muted-foreground` at [AppSidebar.tsx](web/dash0/src/components/layout/AppSidebar.tsx) l.214 and l.321
- `bg-muted` at l.313
- `hover:bg-accent` in `ThemeToggle` and `LanguageSwitcher`
- `text-muted-foreground` in `ServerVersionIndicator`

Don't chase them one by one. Put the `dark` class on the sidebar container.
Both the `.dark { --tokens }` block and
`@custom-variant dark (&:is(.dark *))` in
[index.css](web/dash0/src/index.css) apply to that subtree, so every nested
token and `dark:` variant resolves to its dark value. Three details:

- **Mobile**: the sidebar renders inside a `Sheet` (`sidebar.tsx`, via
  `SheetContent`). The class must be on the sheet content too, not only on the
  desktop container.
- **Portals**: dropdowns opened from the sidebar (the user menu with the org
  switcher, AppSidebar l.288-393) portal to `body`. They stay in the page's
  theme, which is correct. A light menu opening from a dark sidebar is normal.
- **Theme toggle**: it must keep reading the *document* theme, not the
  sidebar's. Check that `use-is-dark-theme.ts` and the toggle look at
  `document.documentElement`, which they do today, and that the icon it shows
  is still right in light mode.

**Sidebar tokens.** They are the same in both themes. Dark mode is only a
little deeper.

| Token | Light-mode value | Dark-mode value |
|---|---|---|
| `--sidebar` (solid fallback) | `oklch(0.18 0.05 263)` | `oklch(0.125 0.035 263)` |
| `--sidebar-gradient` (new) | `linear-gradient(180deg, oklch(0.21 0.055 262), oklch(0.145 0.04 265))` (`#0a1731` to `#04091b`) | `linear-gradient(180deg, oklch(0.14 0.04 262), oklch(0.11 0.03 265))` |
| `--sidebar-foreground` | `oklch(0.92 0.02 255)` | same |
| `--sidebar-muted-foreground` (new) | `oklch(0.66 0.04 255)` | same |
| `--sidebar-accent` (hover) | `oklch(1 0 0 / 0.05)` | same |
| `--sidebar-accent-foreground` | `oklch(0.98 0.01 255)` | same |
| `--sidebar-active` (new) | `linear-gradient(90deg, oklch(0.62 0.2 245 / 0.38), oklch(0.62 0.2 245 / 0.06))` | same |
| `--sidebar-primary` (the active marker; defined today but unused) | `oklch(0.75 0.14 220)` (`#00c1eb`) | same |
| `--sidebar-border` | `oklch(0.27 0.05 262)` | `oklch(0.22 0.035 262)` |
| `--sidebar-ring` | `oklch(0.72 0.15 252)` | same |

**Where to declare these tokens.** Declare the sidebar tokens in `:root` and
`:root.dark` (the `<html>` element) only, **not** in the generic `.dark`
block. The sidebar element itself now carries `class="dark"`. Any `--sidebar-*`
value declared under `.dark` would be re-applied on that element and pin the
sidebar to its dark-mode values in light mode too. Declared on `<html>`, they
simply inherit. Add a check: in light mode, the computed `--sidebar` on the
sidebar element equals the light-mode value.

**Sidebar primitive fixes** ([sidebar.tsx](web/dash0/src/components/ui/sidebar.tsx)):

- Paint the surface with `bg-sidebar` plus the gradient as
  `background-image`, with a `@utility bg-sidebar-gradient`.
- **Active item.** Set `data-[active=true]` to use `--sidebar-active` as the
  background, `--sidebar-accent-foreground` text and `font-semibold`. Add a
  3px `--sidebar-primary` bar on the left edge (a `::before`, rounded on the
  inside corners only). Hover stays the flat 5% white wash.
- **Group labels** use `--sidebar-muted-foreground` instead of
  `text-sidebar-foreground/70` (l.376).
- **Right edge**: l.232 is a bare `border-r`, which resolves to `--border`.
  Change it to `border-sidebar-border`.
- **l.450** has `hsl(var(--sidebar-border))`, which is invalid against oklch
  tokens. Use `var(--sidebar-border)`.
- **Collapsed icon rail**: the active marker and the tooltip must still work
  there. `e2e/sidebar.spec.ts` l.296-310 pins the 48px rail width and label
  CSS. Don't change the geometry.

**AppSidebar content.**

- The logo sits on navy now. The crimson mark reads well there (see the mock),
  so don't add a white tile behind it.
- The org name line uses `--sidebar-muted-foreground`.
- Count pills (checks, incidents) are optional in this spec. If added, use
  the mock's style: a 5% white pill, and the destructive tint for open
  incidents.
- [live-status-dot.tsx](web/dash0/src/components/layout/live-status-dot.tsx)
  l.12-15 uses `bg-gray-300` for the idle state, which disappears on navy.
  Switch it to a token that is visible on both surfaces.

### 2. Page header: gradient icon tile and heavier title

In [page-header.tsx](web/dash0/src/components/shared/page-header.tsx):

- **Tile**:
  - `bg-accent-gradient` with `--gradient-foreground` icon color.
  - `rounded-lg` (up from `rounded-md`), 40px, as today.
  - A soft tinted drop shadow, like `--shadow-primary` but tighter:
    `0 8px 18px -8px color-mix(in oklab, var(--primary) 70%, transparent)`.
- **Title**: `text-2xl font-bold tracking-[-0.025em]`.
  - `listing-pages-style.spec.ts` (l.91, l.147) only checks `text-2xl`, which
    still passes.
  - Update any test that asserts `font-semibold` on the page `h1`, such as
    `incident-notifications.spec.ts:107`. The change is deliberate.
- **Neutral tone for brand logos.** The only `iconClassName` override today,
  [integrations.$integrationUid.tsx:327](web/dash0/src/routes/orgs/$org/integrations.$integrationUid.tsx:327),
  passes `bg-transparent` to show a provider logo. That no longer clears a
  gradient (see the gotcha in spec 01 §2). Add an explicit
  `tone?: "brand" | "neutral"` prop. The default is `"brand"`. `"neutral"`
  renders today's `bg-muted` tile. Use it for third-party logos, which must
  keep their own colors on a neutral tile.
- **Scope**: PageHeader is used by 27 route files. About 29 route files
  render their own `<h1>` without it (check detail, incident detail, status
  page detail, on-call, escalation policies, ...). Converting them is **out
  of scope**. Many of those headers carry status and actions in a different
  shape. List them in the PR description as a follow-up.

### 3. Page glow

- Add one decorative layer at the top of the content area, in
  [$org.tsx](web/dash0/src/routes/orgs/$org.tsx) around the `SidebarInset`
  (l.1242-1278).
  - Absolutely positioned, `pointer-events-none`, `aria-hidden`.
  - Painted with `--page-glow`, about 260px tall, behind the content
    (`z-0`, with the content on `relative z-10`, or `isolate`).
- It must not scroll the page, cover a click target, or show up in print
  (`print:hidden`).
- The `h-12 border-b` top bar (l.71) stays. It can take a very light
  translucent background (`bg-background/60 backdrop-blur`) so the glow shows
  through. Keep it only if it does not blur the breadcrumbs.

### 4. Hero KPI tile

Extract the local `KpiTile`
([dashboard-page.tsx:790-836](web/dash0/src/components/dashboard/dashboard-page.tsx:790))
into `web/dash0/src/components/shared/kpi-tile.tsx`, with a
`variant?: "default" | "hero"` prop, and add it to the design reference. The
current "KPI tiles" section is hand-written markup. Make it import the real
component.

The `hero` variant:

- Background `bg-hero-gradient`, no border, `--gradient-foreground` text.
  The label and sub text are white at about 80% alpha. The icon chip is
  `bg-white/15`.
- A tinted shadow, `0 14px 28px -12px color-mix(in oklab, var(--primary) 60%, transparent)`.
- **Status stays legible.** The tier badge (`AVAILABILITY_TIER_CLASSES`,
  dashboard-page.tsx l.234-254) becomes a solid `bg-white` chip, keeping
  the tier's *light-theme* text color (emerald-700, amber-700, destructive).
  Do this in both themes. A pale translucent status badge on the gradient
  would lose its meaning.

On the dashboard, the hero is the **24h availability** tile
(`data-testid="kpi-tile-availability"`). It is the headline number. The rule
is one hero tile per page. Document that rule on the design reference.

The hover lift already on the tiles
(`hover:-translate-y-0.5 hover:shadow-card-hover`) stays. On the hero, the
hover shadow uses the tinted shadow above, not `shadow-card-hover`. Put the
translate behind `motion-safe:`.

Not in this spec: a sparkline on the hero tile (it is in the mock, but it
needs a series the endpoint does not return), and hero tiles on other pages.

### 5. Design reference

- **New "App chrome" section**: a static, non-interactive replica of the dark
  sidebar (a few items, one active, one group label, the footer row) in both
  themes. Also the page header with the brand and neutral tones, and the page
  glow.
- **"Page header" section** (l.646-984): update it for the gradient tile and
  the `tone` prop.
- **"KPI tiles" section** (l.5267-5335): render the real `KpiTile`, default
  and hero.

## Out of scope

- Converting the ~29 hand-rolled `<h1>` headers to `PageHeader`. List them in
  the PR as a follow-up.
- Auth screens and the aurora panel: spec 03.
- Changing the sidebar structure, the items or the collapse behaviour.

## Acceptance criteria

- [ ] The sidebar is dark navy in both themes, on desktop, in the collapsed
      rail and in the mobile sheet. Nothing inside it uses a light-theme
      token.
- [ ] The active item shows the gradient wash and the cyan left marker.
- [ ] `sidebar.tsx` uses `border-sidebar-border` and no `hsl(var(--...))`.
- [ ] `PageHeader` renders the gradient tile by default and a neutral tile
      with `tone="neutral"`. The integration detail page uses `neutral`.
      The `h1` is `font-bold`.
- [ ] The page glow shows on every org route. It is not clickable, not
      printed, and causes no layout shift.
- [ ] `KpiTile` lives in `components/shared/`. The availability tile renders
      the hero variant, and its tier badge stays readable on the gradient.
- [ ] The design reference documents the sidebar, page header, glow and KPI
      tile variants, including the one-hero-per-page rule.
- [ ] The dashboard is usable on mobile (375px): the hero tile stacks, and the
      sheet sidebar is dark.

## Verification

- `make lint-dash0` (no new errors) and `bun run test:unit` in `web/dash0`.
- e2e:
  - `sidebar.spec.ts`
  - `dashboard.spec.ts` (the `kpi-tile-*` test ids must survive the extraction)
  - `listing-pages-style.spec.ts`
  - `incident-notifications.spec.ts`
  - `dark-mode.spec.ts`
- Screenshots of the dashboard, checks list and integration detail in both
  themes, plus the dashboard at 375px, in the PR.

## Implementation Plan

1. **Sidebar tokens** (`web/dash0/src/index.css`): move every `--sidebar-*`
   out of the generic `.dark` block. Light values stay in `:root`; the deeper
   dark-mode values go in a new `:root.dark` block (the `<html>` element only),
   so the `dark` class on the sidebar element never re-applies them. Add
   `--sidebar-gradient`, `--sidebar-muted-foreground`, `--sidebar-active`, the
   `--color-sidebar-muted-foreground` mapping and `@utility bg-sidebar-gradient`
   / `bg-sidebar-active`. `--sidebar-primary-foreground` (not in the table,
   unused) becomes the navy so it stays readable on the cyan marker. Teach
   `cn()` the two new gradient utilities. Unit test: parse the blocks, check
   the table values, that `.dark` declares no `--sidebar-*`, and the text
   contrasts on the navy.
2. **Sidebar primitive** (`ui/sidebar.tsx`): `dark` class on the desktop
   `data-slot="sidebar"` root, the mobile `SheetContent` and the
   `collapsible="none"` root; surfaces `bg-sidebar bg-sidebar-gradient`;
   right edge `border-sidebar-border` (mobile sheet too); active item =
   `--sidebar-active` image + accent foreground + `font-semibold` + a 3px
   `--sidebar-primary` `::before` bar flush with the sidebar edge (rounded on
   the inside corners), hover stays the flat 5% wash; group labels use
   `text-sidebar-muted-foreground`; `outline` variant uses `var(--sidebar-*)`
   without `hsl()`. Rail geometry untouched.
3. **AppSidebar content**: org name and user subtitle on
   `text-sidebar-muted-foreground`, avatar fallback on a 10% white chip
   (the dark `--muted` is ~1:1 on the navy) with a `data-testid`,
   `LiveStatusDot` idle state on `bg-muted-foreground` (visible on the page
   and on the navy). Theme toggle keeps reading `html`. Count pills skipped
   (optional).
4. **Page header**: `tone?: "brand" | "neutral"` (default brand):
   brand = `bg-primary bg-accent-gradient text-gradient-foreground
   rounded-lg shadow-tile` (new `--shadow-tile` theme shadow), neutral =
   today's `bg-muted text-foreground`; `h1` = `text-2xl font-bold
   tracking-[-0.025em]`. Integration detail uses `tone="neutral"`. Update
   `incident-notifications.spec.ts:107` to `font-bold`.
5. **Page glow** (`routes/orgs/$org.tsx`): an `aria-hidden`,
   `pointer-events-none`, `print:hidden` absolute layer painted with
   `--page-glow`, 260px tall at the top of `SidebarInset`; header and content
   become `relative` so they paint over it (no new stacking context against
   the fixed sidebar). Top bar gets `bg-background/60 backdrop-blur`.
6. **KpiTile** → `components/shared/kpi-tile.tsx` with
   `variant?: "default" | "hero"`; hover lift built in behind
   `motion-safe:`. Hero = `bg-hero-gradient` (token untouched), no border,
   white value, `shadow-hero` (new theme shadow) at rest and on hover,
   `bg-white/15` icon chip. **Hero text rule resolution**: the tile crops the
   gradient (`bg-size-[180%_180%] bg-bottom-right`) so it only shows the
   darker 56% of `--hero-gradient` (t ≥ 0.444); the small label and sub are
   white at 90% (not 80%): composited over the lightest visible color that is
   ≥ 4.69:1. 80% would need t ≥ 0.56 everywhere, i.e. the light cyan could
   never be under any small text, which the tile's layout cannot guarantee at
   172px wide. Unit test computes the rendered contrast from the real classes
   and the parsed token. Dashboard: availability tile = hero, its tier badge
   a solid `bg-white` chip with emerald-700 / amber-700 / red-700 (the light
   `--destructive` is 4.41:1 on white, under 4.5 for 11px) / slate-600 text,
   same in both themes. `kpi-tile-*` test ids stay on the wrappers.
7. **Design reference**: new "App chrome" section (static, `inert` sidebar
   replica with real primitives, group label, active item, footer row; the
   page header in both tones; the page glow), "Page header" copy for the
   gradient tile + `tone`, "KPI tiles" renders the real `KpiTile` default +
   hero with the one-hero-per-page rule and the crop rule; the gradient text
   rule mentions the hero tile's crop.
8. **Tests**: unit (tokens, sidebar classes, PageHeader tones, KpiTile
   classes + rendered hero contrast, tier chips); e2e
   `electric-identity-app-chrome.spec.ts` (sidebar navy in light mode incl.
   computed `--sidebar` on the element, active marker + wash, collapsed rail
   marker, mobile sheet dark at 375px, header tile gradient/neutral + bold h1,
   glow not clickable / not printed / no layout shift, hero tile + white
   badge, 375px stacking); update `sidebar.spec.ts` (avatar fallback test id)
   and `incident-notifications.spec.ts`.
