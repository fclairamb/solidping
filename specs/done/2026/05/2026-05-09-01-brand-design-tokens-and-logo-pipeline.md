# Brand design tokens and logo asset pipeline

## Context

The marketing site at [solidping.io](https://www.solidping.io) leans on a
crimson / magenta-pink hero (full-bleed gradient, white logo + wordmark, pink
buttons). The two product surfaces — `web/dash0` (operator dashboard) and
`web/status0` (public read-only status page) — currently ignore that
identity:

- Both apps use a **blue** Tailwind v4 design-token theme defined in
  `web/dash0/src/index.css` and `web/status0/src/index.css`
  (`--primary: oklch(0.55 0.18 250)`).
- Both surfaces use the generic Lucide `Activity` icon as a stand-in for the
  logo:
  - `web/dash0/src/components/layout/AppSidebar.tsx:184` (sidebar header).
  - `web/dash0/src/routes/orgs/$org/login.tsx:404` (login screen).
- `web/dash0/index.html` references `<link rel="icon" href="logo.png">` but
  no `logo.png` exists in `web/dash0/public/` — so the favicon currently
  404s.
- `web/status0/index.html` references `/status0/logo.png`, also missing from
  `public/`.
- The actual logo asset lives at `res/logo.png` and `res/logo_256.png` at
  the repo root. There is no SVG version. PNG forces us into pixel-grid
  sharpness compromises at sidebar / favicon sizes (16, 24, 32, 64).

The result: someone landing on solidping.io and clicking "Get started" hits
a blue dashboard with a generic icon. Visual continuity is broken; brand
recall is weak.

## What we are NOT doing

- We are **not** repainting the dashboard pink. Operator UIs need
  semantically reserved color: green = up, yellow = degraded, red = down.
  Crimson/magenta-pink sits one hue step from incident-red and would
  compete with the alert signal. See the `Color philosophy` section below
  for the rule we're committing to.
- We are **not** changing the marketing site itself — that lives outside
  this repo and stays the brand-forward "loud" surface.
- We are **not** introducing a separate design-system package. Tokens stay
  in the per-app `index.css` for now; we duplicate them into both apps and
  keep them in sync by convention. Extracting a shared package is a
  separate refactor when a third surface appears.

## Goal

Three things, all foundational and all consumed by specs `02` (dash0) and
`03` (status0):

1. **A vector logo asset** (`res/logo.svg`) plus a small set of derivative
   PNG sizes, made available to each app's build pipeline.
2. **Brand color tokens** added to each app's `index.css`, exposed as
   `--brand`, `--brand-foreground`, and `--brand-muted` — distinct from
   `--primary` (action color), `--destructive` (delete/error), and
   `--status-error` (down-state). All five must remain visually
   distinguishable side-by-side.
3. **A `<Logo />` component** in each app that picks the right asset for
   the rendering size and adapts to light/dark mode, replacing every
   hard-coded `<Activity />` placeholder.

## Color philosophy (the rule we apply across all three specs)

Five color roles, never collapsed:

| Token             | Purpose                                  | Approx hue |
|-------------------|------------------------------------------|-----------|
| `--brand`         | Logo bg, marketing-link accents, login   | ~5–10 (crimson) |
| `--primary`       | Buttons, links, focus rings, charts      | ~250 (blue) — unchanged |
| `--destructive`   | Delete, irreversible action confirms     | ~25 (orange-red) — unchanged |
| `--status-error`  | "Check is down" badge                    | ~25 (red) — unchanged |
| `--status-warning`| "Degraded" badge                         | ~85 (amber) — unchanged |

The brand color is never used for **interactive affordance** in the
operator UI. It paints chrome (logo tile, header strips, login card top
border) — never buttons or active-row highlights. This is the rule that
keeps dashboard alerts legible and prevents brand-vs-status color
collision.

`status0` is allowed to use `--brand` more generously (header bar,
hyperlinks back to solidping.io) because subscribers don't read it as an
ops console.

## Token values

Add to both `web/dash0/src/index.css` and `web/status0/src/index.css`,
under the existing `:root` block, immediately after the existing
`--primary` declarations:

```css
:root {
  /* Brand: crimson/magenta — solidping.io identity */
  --brand: oklch(0.58 0.22 5);
  --brand-foreground: oklch(0.98 0.01 5);
  --brand-muted: oklch(0.92 0.05 5);
}

.dark {
  --brand: oklch(0.65 0.20 5);
  --brand-foreground: oklch(0.15 0.02 5);
  --brand-muted: oklch(0.30 0.08 5);
}
```

Plus the matching `@theme inline` exposure so Tailwind v4 emits utility
classes (`bg-brand`, `text-brand`, `border-brand`, etc.):

```css
@theme inline {
  /* ...existing... */
  --color-brand: var(--brand);
  --color-brand-foreground: var(--brand-foreground);
  --color-brand-muted: var(--brand-muted);
}
```

The exact oklch values must be eyeballed against the live solidping.io
hero (which uses an unknown gradient — best-effort visual match, not a
spec'd HEX). Calibrate by overlaying the dash0 dev server with a screenshot
of solidping.io and tweaking until the pinks read as siblings, not twins.

## Logo asset pipeline

### Step 1: produce `res/logo.svg`

Today only PNG (`res/logo.png`, `res/logo_256.png`) exists. Vectorize the
shield + heart-pulse mark:

- Hand-author the SVG (two paths: the shield outline + the pulse line) at
  a 256×256 viewBox to match the PNG's resolution.
- The SVG must use `currentColor` for the foreground stroke/fill so the
  same asset renders correctly on a brand-pink tile, on a white tile, and
  monochrome in a sidebar.
- Commit at `res/logo.svg` alongside the existing PNGs.

If hand-authoring is impractical, run the existing PNG through a vector
tracer (e.g. `vtracer`) and clean up the result manually. Avoid embedded
raster — the whole point is sharpness at small sizes.

### Step 2: derive a favicon set

Generate from the SVG:

- `favicon.svg` — the SVG itself, copied as-is.
- `favicon-32.png`, `favicon-192.png`, `favicon-512.png` — for browser tab,
  PWA install, and home-screen icons.
- `apple-touch-icon.png` (180×180) — iOS home screen.

Use a one-shot script (`scripts/build-favicons.sh`) that calls `rsvg-convert`
or `magick` so re-generating is cheap. Commit the generated PNGs (small,
build-determinism over re-running a tool everyone has to install).

### Step 3: drop assets into each app's `public/`

For both `web/dash0/public/` and `web/status0/public/`:

- `logo.svg`
- `logo-32.png`, `logo-64.png`, `logo-256.png`
- `favicon.svg`, `favicon-32.png`, `favicon-192.png`, `favicon-512.png`
- `apple-touch-icon.png`

A small `Makefile` target (`make sync-brand-assets`) copies from `res/` into
both `public/` directories so they don't drift. Wire it into `make build`
so a fresh checkout produces the assets.

### Step 4: update `index.html` favicon links

Replace the single `<link rel="icon" type="image/png" href="logo.png" />`
in both `web/dash0/index.html` and `web/status0/index.html` with the
modern set:

```html
<link rel="icon" type="image/svg+xml" href="favicon.svg" />
<link rel="icon" type="image/png" sizes="32x32" href="favicon-32.png" />
<link rel="apple-touch-icon" href="apple-touch-icon.png" />
<link rel="manifest" href="manifest.webmanifest" />
```

`status0` paths are prefixed with `/status0/` per its existing convention.

Also add a minimal `manifest.webmanifest` next to the favicons so PWA
installs pick the right icon and theme color (set `theme_color` to the
brand pink, `background_color` to the dashboard background — the bridge
device users see when launching from a home screen).

## `<Logo />` component

In each app, add `src/components/ui/logo.tsx`:

```tsx
type LogoProps = {
  size?: number;            // px — default 32
  variant?: "mark" | "wordmark"; // mark = icon only, wordmark = icon + "SolidPing"
  className?: string;
};

export function Logo({ size = 32, variant = "mark", className }: LogoProps) {
  // Renders <img src="/logo.svg"> at the requested size, with proper
  // base-URL prefixing (read import.meta.env.BASE_URL).
  // Wordmark variant adds the "SolidPing" text in font-semibold next to it.
}
```

Reasons it's a component, not a raw `<img>`:

- The `BASE_URL` differs between dev (`/`) and prod (`/dash0/`,
  `/status0/`) — centralize the path-prefixing so consumers don't get it
  wrong.
- It encapsulates the size-vs-asset choice (use the SVG up to ~64px, fall
  back to a higher-res PNG only if SVG rendering is glitchy in some
  edge browser — for now, SVG everywhere).
- Future dark-mode tweaks (e.g. inverted wordmark color) live in one
  place.

Both apps get their own copy. Do not extract into a shared package yet
(see "What we are NOT doing").

## Wire-up checklist (foundation only — no UI surface changes)

This spec lands invisible plumbing. The visible replacement of `<Activity />`
with `<Logo />` is in specs `02` and `03`.

- [ ] `res/logo.svg` committed (hand-authored or traced + cleaned).
- [ ] `scripts/build-favicons.sh` produces the favicon PNG set.
- [ ] `make sync-brand-assets` copies `res/logo.svg` and the favicon set
      into `web/dash0/public/` and `web/status0/public/`.
- [ ] `--brand`, `--brand-foreground`, `--brand-muted` tokens added in
      both `index.css` files (light + dark).
- [ ] `@theme inline` exposes the new tokens to Tailwind utilities.
- [ ] `index.html` favicon links updated in both apps.
- [ ] `manifest.webmanifest` added in both apps.
- [ ] `<Logo />` component exists in both `src/components/ui/logo.tsx`
      files and renders correctly at sizes 16, 24, 32, 48, 64, 96.
- [ ] Dash0 design-reference page (`src/routes/orgs/$org/design-reference.tsx`)
      adds a "Brand" section showing the `<Logo />` variants and a swatch
      for `--brand` / `--brand-muted` / `--brand-foreground` next to the
      existing color swatches, so the rule "brand ≠ primary ≠ destructive ≠
      status-error" is visible at a glance.

## Verification

- `bun run build` succeeds in both `web/dash0` and `web/status0`.
- Loading the dashboard at `/dash0/` shows the new favicon in the browser
  tab (no 404 in DevTools network).
- The design-reference page renders the four color swatches side-by-side
  in light mode and dark mode without any pair feeling like a duplicate.
- Visual regression: log in as `admin@solidping.com` / `solidpass`, walk
  the sidebar — no `<Activity />` icon swap yet (that's spec `02`), but the
  favicon should be the SolidPing mark.

## Out of scope, deferred

- Marketing site changes (different repo).
- Email template branding (transactional emails currently send plain text
  / minimal HTML — separate spec when we revisit notification design).
- Customer-customizable status page branding (already partly supported —
  this spec only changes the *default* template).
- Extracting design tokens into a shared workspace package.
