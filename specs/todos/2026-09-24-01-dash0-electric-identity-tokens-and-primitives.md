---
model: opus
effort: high
---

# dash0 looks like a stock open-source theme: adopt the "electric blue" identity (tokens and primitives)

This is spec 1 of the "electric identity" series. It owns the palette. Every
other spec in the series takes its colors from the tables below.

| # | Spec | Depends on |
|---|---|---|
| 01 | dash0 tokens and primitives (this spec) | – |
| 02 | [dash0 app chrome](2026-09-24-02-dash0-electric-identity-app-chrome.md): dark sidebar, page header tile, page glow, hero KPI tile | 01 |
| 03 | [Auth screens](2026-09-24-03-auth-screens-electric-aurora.md): aurora panel, login, signup | 01 |
| 04 | [Public status pages](2026-09-24-04-status0-electric-identity.md) (status0) | palette only |
| 05 | [Emails](2026-09-24-05-emails-electric-identity.md) | palette only |
| 06 | [Docs site](2026-09-24-06-docs-site-electric-identity.md) | palette only |
| – | Website: `solidping-website/specs/todos/2026-09-24-01-website-electric-identity.md` (other repo) | palette only |

## Problem

The dashboard reads like a basic open-source project. The request, verbatim:
*"The current design of solidping looks a bit blend, like a basic
open-source project. [...] give it a much stronger identity with some
stronger colors. It needs to be much more modern (gradients and such, but not
too much) and affirmative."*

The theme in [index.css](web/dash0/src/index.css) is shadcn defaults with a
blue tint:

- `--primary` is a mid blue, `oklch(0.55 0.18 250)`, applied as a flat fill.
  The only gradient in the operator UI is the onboarding checklist's progress bar
  ([onboarding-checklist.tsx:277](web/dash0/src/components/dashboard/onboarding-checklist.tsx:277)).
- Neutrals are slate at chroma 0.005 to 0.01. Cards are white on an
  almost-white page. Nothing on screen carries a signature.
- The sidebar is near-white (`--sidebar: oklch(0.985 0.004 250)`), so the
  whole app is one light, low-contrast plane.
- The crimson `--brand` token is only visible in the logo. The browser
  `theme-color` is still crimson `#e91e63` ([index.html:15](web/dash0/index.html:15)).

## Decision (2026-09-24)

Four directions were mocked on a replica of the dashboard: Crimson (brand pink
everywhere), Sunset (pink to violet), Ink and crimson (near-black and
crimson accents), and Electric blue. The mock is saved at
[wiki/design/2026-09-24-identity-directions.html](wiki/design/2026-09-24-identity-directions.html).
Open it with `?dir=electric`. It is the visual target for this series, so open
it before you start.

Chosen:

- **Electric blue.** Primary actions use a cyan to blue to indigo gradient.
  Neutrals take a blue tint.
- **The sidebar is always dark navy**, in both themes. Spec 02 builds it.
  This spec only defines the tokens.
- **Headings stay Inter.** Weight and tracking get tighter (spec 02).
- **The logo stays crimson.** `--brand` keeps its current value and meaning:
  logo and brand chrome only. It becomes the one warm accent in a blue UI.
- **Gradients are rationed.** They go on primary actions, the few "on" states
  of controls, the page header icon tile, one hero tile per page, and a faint
  glow at the top of the page. Tables, cards, forms and popovers stay flat.

## Proposal

### 0. Work from the design reference

The design reference page
([design-reference.tsx](web/dash0/src/routes/orgs/$org/design-reference.tsx),
live at `/d/orgs/<org>/design-reference`) is the single source of truth for
dash0 UI. Land the tokens and primitives there first:

1. Change the tokens.
2. Check every section of the page in light and dark.
3. Only then look at real routes.

Every new token and utility gets a swatch or example on the page (see §5).

### 1. Base tokens (`web/dash0/src/index.css`)

Replace the values below. Tokens not listed keep their current values:
`--brand*`, `--destructive`, `--status-*`, `--chart-2..4` and `--radius`.
The status colors do not collide with electric blue, so the green, amber and
red semantics stay exactly as they are.

Light (`:root`):

| Token | New value | Approx. hex |
|---|---|---|
| `--background` | `oklch(0.98 0.006 250)` | `#f5f9fc` |
| `--foreground`, `--card-foreground`, `--popover-foreground` | `oklch(0.18 0.03 258)` | `#09121f` |
| `--card`, `--popover`, `--control` | `oklch(1 0 0)` | `#ffffff` |
| `--primary` | `oklch(0.55 0.22 262)` | `#1e64ef` |
| `--primary-foreground` | `oklch(0.99 0 0)` | white |
| `--secondary`, `--muted` | `oklch(0.962 0.01 255)` | `#eef3f9` |
| `--secondary-foreground` | `oklch(0.26 0.04 258)` | |
| `--muted-foreground` | `oklch(0.5 0.03 255)` | `#586474` |
| `--accent` | `oklch(0.95 0.03 255)` | `#e1f0ff` |
| `--accent-foreground` | `oklch(0.42 0.18 262)` | `#0a41ad` |
| `--border` | `oklch(0.915 0.012 255)` | `#dee3eb` |
| `--input` (a border color, not a fill) | `oklch(0.87 0.015 255)` | |
| `--ring` | `oklch(0.55 0.22 262)` | `#1e64ef` |
| `--chart-1` | same as `--primary` | |
| `--chart-5` | `oklch(0.55 0.2 285)` | `#6b55df` |

Dark (`.dark`):

| Token | New value | Approx. hex |
|---|---|---|
| `--background` | `oklch(0.145 0.02 262)` | `#060a13` |
| `--foreground` (and card/popover fg) | `oklch(0.96 0.01 255)` | |
| `--card`, `--popover` | `oklch(0.185 0.026 262)` | `#0c131e` |
| `--control` | `oklch(0.165 0.024 262)` | |
| `--primary` | `oklch(0.72 0.15 252)` | `#57a8ff` |
| `--primary-foreground` | `oklch(0.16 0.03 262)` | `#060d1a` |
| `--secondary`, `--muted` | `oklch(0.225 0.03 262)` | |
| `--muted-foreground` | `oklch(0.7 0.03 255)` | |
| `--accent` | `oklch(0.3 0.07 260)` | |
| `--accent-foreground` | `oklch(0.92 0.05 255)` | |
| `--border` | `oklch(0.28 0.035 262)` | |
| `--input` | `oklch(0.33 0.04 262)` | |
| `--ring` | `oklch(0.72 0.15 252)` | |
| `--chart-1` | same as `--primary` | |
| `--chart-5` | `oklch(0.65 0.17 285)` | |

Why the dark `--primary` is a *light* blue: `text-primary` (links, the active
tab, the link-variant button) is far more common than solid `bg-primary`
fills. `#57a8ff` on the dark background is about 8:1. The gradient carries
the saturated blue on buttons (§2). Solid `bg-primary` fills in dark mode keep
dark text, as they do today.

The existing `control-surface-elevation.spec.ts` ordering still holds with
these values:

- Light: background (0.98) < control (1) ≤ card (1).
- Dark: background (0.145) < control (0.165) < card (0.185).

### 2. Gradient and glow tokens

Add these to both `:root` and `.dark`. The gradients are the same in both
themes. The glow is stronger in dark mode.

| Token | Value | Used by |
|---|---|---|
| `--primary-gradient` | `linear-gradient(135deg, oklch(0.56 0.17 242) 0%, oklch(0.53 0.22 262) 50%, oklch(0.48 0.23 276) 100%)` | Everything that carries **text** on a gradient: default button, default badge |
| `--accent-gradient` | `linear-gradient(135deg, oklch(0.7 0.15 225) 0%, oklch(0.57 0.22 258) 50%, oklch(0.5 0.23 275) 100%)` | Decorative fills with **no text**: page header icon tile (spec 02), switch, checkbox, progress bar, stepper dots |
| `--hero-gradient` | `linear-gradient(135deg, oklch(0.62 0.17 232), oklch(0.5 0.23 265) 60%, oklch(0.38 0.19 280))` | The hero KPI tile (spec 02) |
| `--page-glow` light | `radial-gradient(45% 200px at 12% 0, oklch(0.7 0.15 225 / 0.13), transparent 70%), radial-gradient(40% 180px at 65% 0, oklch(0.5 0.23 275 / 0.09), transparent 70%)` | The top-of-page glow (spec 02) |
| `--page-glow` dark | same shape, alphas `0.2` and `0.15`, heights `220px` / `200px` | |
| `--gradient-foreground` | `oklch(0.99 0 0)` in both themes | Text and icons on any of the gradients |

Two gradients exist because of contrast. White text on the brighter
decorative start stop (`#00b0e4`) is about 2.5:1. On the `--primary-gradient`
stops it is 4.4:1 at the start, 5.5:1 in the middle and 7.2:1 at the end.
So button labels only ever sit on `--primary-gradient`.

Expose them as Tailwind v4 utilities, so components never write
`bg-[image:var(--...)]` inline:

```css
@utility bg-primary-gradient { background-image: var(--primary-gradient); }
@utility bg-accent-gradient  { background-image: var(--accent-gradient); }
@utility bg-hero-gradient    { background-image: var(--hero-gradient); }
```

Gotcha: `bg-transparent` / `bg-muted` only set `background-color`. An element
carrying one of these utilities keeps the gradient unless the override also
sets `bg-none`. Mention this in the design-reference example.

Also define the missing `--chart-degraded` token.
[response-time-chart.tsx:1284](web/dash0/src/components/checks/response-time-chart.tsx:1284)
reads `var(--chart-degraded, #d97706)`, but the token does not exist anywhere,
so the fallback always applies. Set it to the `--status-warning` value in each
theme.

### 3. Primitives

All in `web/dash0/src/components/ui/`.

- **Button, default variant**
  ([button.tsx](web/dash0/src/components/ui/button.tsx)):
  - Fill: `bg-primary-gradient text-[var(--gradient-foreground)]`.
  - Shadow: a 1px inner top highlight
    (`inset 0 1px 0 rgb(255 255 255 / .22)`) plus the existing
    `shadow-primary` / `shadow-primary-hover`. Those are already tinted from
    `--primary` via `color-mix`, so they follow the new hue.
  - Hover: a slight brightness lift (`hover:brightness-105`) and
    `hover:-translate-y-px`. Put the translate behind
    `motion-safe:`.
  - Keep `bg-primary` as the `background-color` underneath, so the button
    still reads as blue if a gradient ever fails to paint.
- **Button, other variants.**
  - `outline` and `secondary` stay flat. They pick up the stronger `--input`
    border automatically.
  - `destructive` is unchanged: red, `Trash2`, per the delete convention in
    the root `CLAUDE.md`.
  - `link` follows `--primary`.
- **Focus**: inputs, textarea and the select trigger show
  `border-ring` plus a 3px ring of `--ring` at about 25% alpha on
  `focus-visible`. Buttons keep a visible ring (today it is
  `ring-1 ring-ring`), and it must stay visible on the gradient. Use a
  `ring-offset-2` against `--background`, or a 2px ring. Pick one and document
  it.
- **Switch** (checked), **checkbox** (checked), the **progress** bar and the
  **stepper**'s done/connector segments: `bg-accent-gradient` over the
  existing `bg-primary`. They carry no text, or only a white check glyph that
  meets the 3:1 non-text contrast rule on the middle stop.
  - `Progress` keeps its `destructiveWhenFull` behaviour. The red fill must
    win over the gradient (`bg-none bg-destructive`).
- **Badge, default variant**: `bg-primary-gradient` with
  `--gradient-foreground` text. Status variants (`status-*/15`) are
  unchanged. `e2e/dashboard.spec.ts` and `status-page-custom-domain.spec.ts`
  assert on those classes.
- **Segmented control and tabs stay neutral.** The selected pill stays
  `bg-card`: `control-surface-elevation.spec.ts` asserts it, and a gradient
  pill beside a gradient button is one gradient too many. The route-level
  [tab-nav.tsx](web/dash0/src/components/shared/tab-nav.tsx) keeps its
  `text-primary` underline, which now reads electric.

### 4. Raw blue classes that mean "primary"

The inventory found 33 `blue-*`, 12 `sky-*` and 4 `indigo-*` Tailwind
classes in about 13 files. They were written against the old blue and will
now sit next to a different one. Triage each occurrence:

- **It means primary, info or interactive** (a link, a selected state, an
  "info" tone): move it to the token (`text-primary`, `bg-primary/10`,
  `border-primary/30`, ...). Known spots:
  - [stat-tile.tsx:9](web/dash0/src/components/shared/stat-tile.tsx:9)
  - [status-update-kind.ts:9](web/dash0/src/lib/status-update-kind.ts:9)
  - [event-display.tsx](web/dash0/src/components/dashboard/event-display.tsx) lines 13, 165, 180
  - [dependency-row.tsx:150](web/dash0/src/components/checks/dependency-row.tsx:150)
  - [checks.index.tsx](web/dash0/src/routes/orgs/$org/checks.index.tsx) lines 566, 2096
  - [dashboard-page.tsx:981](web/dash0/src/components/dashboard/dashboard-page.tsx:981)
  - `status-pages.$statusPageUid.index.tsx:172`
  - `on-call.$uid.index.tsx:55`
  - `server.email-inbox.tsx:385`
  - `empty-state-onboarding.tsx:174-177`
- **It is a category color** (the per-check-type tones in
  [check-type-identity.tsx](web/dash0/src/components/shared/check-type-identity.tsx)
  lines 86, 96 and 100): leave it. Those are categorical identities, not
  brand, and `check-type-identity.test.ts` pins the literal strings.

Do not touch the green, amber, red and emerald classes in this spec. They
carry status meaning and are out of scope.

### 5. Design reference

- **"Color tokens"** (`COLOR_TOKENS` / `CHART_TOKENS`, around l.1256-1339):
  - Add swatches for `--primary-gradient`, `--accent-gradient`,
    `--hero-gradient` and `--gradient-foreground`.
  - Add `--chart-degraded` to the chart swatches.
  - The `Swatch` component needs a variant that paints `background-image`.
- **"Brand"** (l.4295-4406): update the copy. Blue is now the product color,
  and crimson is the logo only. Say it in one sentence, with the reason: the
  logo stays crimson as the single warm accent.
- **"Buttons and badges"**: show the gradient default button next to outline,
  ghost and destructive, in a row, so the one-gradient-per-group rule is
  visible. Add a short "Gradients" note that lists where gradients are allowed
  (the list under *Decision*) and where they are not (cards, tables, forms,
  popovers, dialogs, segmented controls).
- **Elevation**: re-check the "Elevation, aurora & glass" section in both
  themes. Its aurora example changes in spec 03, and the section text must not
  contradict it once both land.

### 6. Browser chrome color

Change `<meta name="theme-color">` in
[index.html:15](web/dash0/index.html:15) and `theme_color` in
[manifest.webmanifest](web/dash0/public/manifest.webmanifest) from
`#e91e63` to the sidebar navy `#0a1731`. The mobile browser bar then matches
the dark sidebar from spec 02. `background_color` becomes `#f5f9fc`.

## Out of scope

- The sidebar, page header, page glow, hero KPI tile and heading weights:
  spec 02.
- The aurora panel and auth screens: spec 03.
- Status colors, the check-type category tones, and the green/amber/red raw
  classes.
- Icons, radius and spacing.
- The favicon and logo assets. The logo stays crimson.

## Acceptance criteria

- [ ] `index.css` carries the token values of §1 and §2 in both themes. No
      component reads a removed token.
- [ ] The default `Button` renders the `--primary-gradient` with white text.
      Its hover lift is motion-safe and its focus ring is visible on the
      gradient.
- [ ] Switch, checkbox, progress and stepper "on" states use the accent
      gradient. A full destructive `Progress` is still red.
- [ ] Every `blue-*` / `sky-*` / `indigo-*` class listed in §4 is either
      migrated to a token or explicitly kept as a category color.
- [ ] `--chart-degraded` is defined, and the response-time chart no longer
      relies on its hex fallback.
- [ ] The design reference shows every new token and utility, and states the
      gradient rules in both themes.
- [ ] `theme-color` and the manifest use `#0a1731`.
- [ ] White text on `--primary-gradient` is ≥ 4.4:1 at every stop (check the
      computed stops, not the oklch literals). `text-primary` on
      `--background` is ≥ 4.5:1 in both themes.

## Verification

- `make lint-dash0` (no new errors: the base already has about 25 react-hooks errors) and `bun run test:unit` in `web/dash0`.
- The e2e specs most likely to notice:
  - `control-surface-elevation.spec.ts`
  - `destructive-button-shadow.spec.ts`
  - `check-detail.spec.ts` (chart-1 ≠ chart-2)
  - `dashboard.spec.ts`
  - `dark-mode.spec.ts`
  - `listing-pages-style.spec.ts`
- A side-by-side of the design reference, before and after, in both themes.
  Put the screenshots in the PR.
