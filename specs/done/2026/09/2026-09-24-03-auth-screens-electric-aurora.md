---
model: opus
effort: medium
---

# The auth screens still wear the old blue and crimson aurora: re-tune them to the electric identity

Spec 3 of the "electric identity" series. It needs
[spec 01](2026-09-24-01-dash0-electric-identity-tokens-and-primitives.md)
(palette, gradient tokens, gradient button). The visual target is
[wiki/design/2026-09-24-identity-directions.html?dir=electric](wiki/design/2026-09-24-identity-directions.html).

## Problem

Login, signup, forgot/reset password, change password and registration
confirmation all render through
[auth-split-layout.tsx](web/dash0/src/components/layout/auth-split-layout.tsx).
On `lg`+ that layout is a dark marketing panel on the left
([aurora-panel.tsx](web/dash0/src/components/ui/aurora-panel.tsx)) and the
form card on the right. The 404 page (`__root.tsx` l.43) and
`no-org.tsx` (l.87) use `AuroraPanel` directly.

The panel was built for the previous identity:

- Base `bg-slate-950`, a neutral grey-black that does not match the navy
  sidebar from spec 02.
- Blobs in `primary/40`, `brand/40` (crimson) and `chart-5/30`, plus a
  `from-primary/25 ... to-brand/25` wash. Half the glow is crimson.
- The headline accent and the feature check icons use a luminous crimson,
  `ACCENT = "text-[oklch(0.82_0.13_5)]"` (auth-split-layout.tsx:10).
  It is hardcoded, not a token.
- Below `lg` the panel is hidden, and the form sits alone on a plain
  background. On a phone, nothing on the login screen says SolidPing apart from
  the logo in the card.

The first screen a new user sees should be the strongest expression of the
identity.

## Proposal

### 1. AuroraPanel

- **Base**: the sidebar navy gradient
  (`linear-gradient(180deg, oklch(0.21 0.055 262), oklch(0.145 0.04 265))`,
  `--sidebar-gradient` from spec 02 if it has landed, otherwise the literal)
  instead of `bg-slate-950`. The login panel and the sidebar a user sees right
  after logging in are then the same surface.
- **Blobs**: cyan (`oklch(0.7 0.15 225)`) at about 40%, `primary` at about
  40%, and the indigo-violet `chart-5` at about 30%. Same sizes, positions and
  blur as today.
- **Wash**: cyan to indigo at about 25%.
- **Crimson leaves the glow.** The logo in the panel stays crimson. It is the
  only warm spot, and it reads well on navy.
- Keep the component's contract: it is always dark whatever the theme,
  children render on a `z-10` layer, and text is white.
- Replace its doc comment ("blobs pull from primary blue, brand crimson,
  chart-5 violet") with the new description.

### 2. The accent color

- Replace the hardcoded crimson `ACCENT` with a token. Add
  `--aurora-accent: oklch(0.85 0.12 220)` (a luminous cyan, readable on navy),
  exposed as a Tailwind color.
- It colors the highlighted word of the headline and the feature check icons.
- White text on the glass card stays as is.

### 3. Mobile and the form column

- Paint `--page-glow` (spec 01) behind the form column at every breakpoint, so
  the glow carries the identity below `lg`, where the aurora panel is hidden.
- Keep the card itself flat. The submit button already picks up the gradient
  from spec 01.
- Below `lg`, show the wordmark (`<Logo variant="wordmark">`) above the card,
  if the page does not already show it. On a 375px screen, the login page
  should say SolidPing before the user reads a field label.

### 4. Design reference

Update the "Elevation, aurora & glass" section: the aurora example, its
description, and a swatch for `--aurora-accent`.

## Out of scope

- `invite.$token.tsx`, a plain page that does not use `AuthSplitLayout`.
  Moving it onto the split layout is a separate change.
- Marketing copy (`auth` i18n `marketing.*`).
- The glass utility.

## Acceptance criteria

- [ ] `AuroraPanel` has no crimson (`brand`) blob or wash. Its base matches
      the sidebar navy.
- [ ] `auth-split-layout.tsx` has no hardcoded oklch. The accent comes from
      `--aurora-accent`.
- [ ] Login, register, forgot password, reset password, change password,
      confirm registration, no-org and the 404 page all render the new panel
      on `lg`+, and the glow plus wordmark below `lg`.
- [ ] Every one of those pages is usable at 375px in both themes.
- [ ] The design reference aurora example matches the shipped component.

## Verification

- `make lint-dash0` (no new errors) and `bun run test:unit`.
- e2e specs that visit the auth pages (login, register, forgot/reset
  password, change password) stay green.
- Screenshots of login at 1440px and 375px, light and dark, in the PR.
