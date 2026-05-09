# Apply brand identity to the operator dashboard (`dash0`)

## Context

Spec `2026-05-09-01-brand-design-tokens-and-logo-pipeline.md` lands the
foundational pieces: SVG logo, favicon set, `--brand` token family, and a
`<Logo />` component. None of that is visible to the user yet — every
on-screen surface still uses the generic Lucide `<Activity />` icon and
the blue-only color theme.

This spec applies the brand surface-by-surface in `web/dash0`. The
operator dashboard is the surface where the **color discipline matters
most**: ops engineers stare at it for hours, scan it for status at a
glance, and need green/yellow/red to read unambiguously. So brand pink
goes on chrome (logo tile, login card edge, marketing-link accents), and
the existing blue `--primary` keeps doing all interactive work (buttons,
links, focus rings, active-row highlights).

## What we are NOT doing

- **Not** changing button color, link color, focus-ring color, active
  navigation highlight, or chart series colors. Those stay blue — they
  are interactive/data signals, not brand chrome.
- **Not** introducing a brand-pink hover state on rows or list items —
  hover stays neutral (`bg-muted`).
- **Not** repainting the destructive button (delete/remove). Destructive
  remains orange-red `--destructive` — close in hue to brand but
  semantically distinct, and we never place them together (a delete
  button is never adjacent to a brand-pink chrome element by layout).
- **Not** doing dark-mode ergonomic tuning beyond the token values
  defined in spec `01`. If the brand pink reads too vibrantly on a dark
  background during walk-through, file a follow-up; do not lower
  saturation in this spec.

## Surfaces to update

### 1. Sidebar header (`AppSidebar.tsx`)

`web/dash0/src/components/layout/AppSidebar.tsx:177-196`

Today:

```tsx
<SidebarMenuButton size="lg" asChild>
  <Link to="/orgs/$org" params={{ org }}>
    <div className="flex aspect-square size-8 items-center justify-center rounded-lg bg-primary text-primary-foreground">
      <Activity className="size-4" />
    </div>
    <div className="flex flex-col gap-0.5 leading-none">
      <span className="font-semibold">SolidPing</span>
      <span className="text-xs text-muted-foreground">{currentOrgName || org}</span>
    </div>
  </Link>
</SidebarMenuButton>
```

Change:

- Replace the `bg-primary` tile + `<Activity />` with the `<Logo />`
  component, sized 32px, on a `bg-brand` rounded tile (`text-brand-foreground`
  for the logo stroke if the SVG uses `currentColor`).
- Keep the wordmark + org name to the right of the tile, unchanged.

This is the single most-visible brand placement in the operator UI: every
authenticated page renders this sidebar.

### 2. Login screen (`login.tsx`)

`web/dash0/src/routes/orgs/$org/login.tsx:399-411`

Today:

```tsx
<Card className="w-full max-w-md">
  <CardHeader className="text-center">
    <div className="flex justify-center mb-4" data-testid="login-logo">
      <Activity className="h-12 w-12 text-primary" />
    </div>
    <CardTitle className="text-2xl" data-testid="login-title">SolidPing</CardTitle>
    ...
```

Change:

- Replace the `<Activity />` with the `<Logo />` component at `size={64}`,
  `variant="mark"`.
- Add a 4-pixel `--brand` color strip across the top of the `<Card>`
  (e.g. `<div className="h-1 bg-brand rounded-t-[inherit]" />` as the
  card's first child, or via `border-t-4 border-t-brand`). This is the
  only piece of pink chrome on the login screen — enough to anchor brand
  identity for a user arriving from solidping.io, not enough to turn the
  login into a marketing page.
- Keep the form, buttons, and links blue (`--primary`).
- Preserve the `data-testid="login-logo"` selector — Playwright tests in
  `web/dash0/e2e/` rely on it.

### 3. Auth shell — register, password-reset, accept-invitation

Mirror the login change on the other unauthenticated routes that share
the same card-on-blank-background layout. Audit:

- `src/routes/register.tsx` (or wherever `/register` lives)
- `src/routes/auth/*` if present (password reset, invitation accept,
  membership-request flows)

Each gets the `<Logo size={64} />` + `border-t-4 border-t-brand` pair.
This makes "I'm in the SolidPing universe" recognition consistent across
every pre-authenticated surface — exactly the moment when the user just
clicked "Get started" on the marketing page and is most sensitive to
visual discontinuity.

### 4. App-loading splash / suspense fallback

If `__root.tsx` (or the suspense boundary in `main.tsx`) currently renders
a bare spinner or empty screen during initial chunk load, replace it with
a centered `<Logo size={48} />` + small `<Spinner />` underneath. This is
a 200ms-to-2s window that today gives the user nothing — adding the mark
makes cold loads feel like the app already arrived.

If no fallback exists, skip — don't invent one for branding alone.

### 5. 404 / error boundary screens

Audit `src/routes/__root.tsx` (or wherever `errorComponent` /
`notFoundComponent` is defined). On the empty-state screen, place a
small `<Logo size={32} />` above the "Page not found" / "Something went
wrong" message. Same reasoning as the loading splash: cheap brand
re-anchoring at moments where the dashboard is not visually itself.

### 6. Bug-report dialog header

The in-app bug-report flow (the modal that posts to
`POST /api/mgmt/report`) is a moment where the user is frustrated and
context-aware that they're talking to "SolidPing the product." A small
`<Logo size={20} />` in the dialog header alongside the title is a low-
risk, high-context placement.

If the bug-report UI doesn't have a clear header slot, skip.

## Surfaces explicitly NOT to touch

- **Sidebar nav items** (Checks, Incidents, Status pages, etc.) — keep
  the `Activity`-style functional Lucide icons. Those are informational,
  not branding.
- **The org dashboard's "Overall status" banner** — keep the
  green/yellow/red semantics. No brand color near status.
- **KPI tiles, chart axes, chart series** — all stay `--primary` blue or
  the existing chart palette.
- **All data tables and list rows** — no brand color in row chrome,
  borders, or hover states.
- **The design-reference catalog** — already gets a "Brand" section per
  spec `01`; that's enough.

## Color collision audit

After the changes, walk the app with both light and dark mode and look
specifically for these failure modes:

1. A `bg-brand` tile placed within ~80px of a `bg-destructive` button —
   the eye reads them as the same family. Move one or kill one.
2. A `text-brand` link adjacent to a `text-status-error` "DOWN" badge.
   Same problem. Use `text-primary` for links here instead.
3. The login screen's `--brand` top strip vibrating against any error
   alert (`bg-destructive/10` style) below it. If it does, drop the strip
   to 2px or change the alert variant.

The discipline: brand pink and incident red **never within a single
visual scan**. If they appear together, one of them is wrong.

## Wire-up checklist

- [ ] `AppSidebar` header tile uses `<Logo size={32} />` on a
      `bg-brand` rounded tile.
- [ ] Login screen uses `<Logo size={64} />` + `border-t-4 border-t-brand`
      strip on the card. `data-testid="login-logo"` preserved.
- [ ] Register / password-reset / invitation-accept screens get the same
      treatment.
- [ ] (Conditional) loading splash and error-boundary screens get a small
      `<Logo />` placement.
- [ ] All buttons, links, focus rings, active-nav highlights, chart
      series colors are unchanged.
- [ ] Color-collision audit walked in light and dark mode.

## Verification

- `make dev-test` — log in as `test@test.com` / `test` and walk:
  sidebar → login (log out, log back in) → register → password reset →
  invitation accept → 404 page (visit `/dash0/orgs/test/does-not-exist`).
- Each pre-auth surface shows the actual SolidPing mark, not Activity.
- The org dashboard's status banner still goes red on a real incident
  (induce one with a dummy down check) and the red is visually
  distinguishable from the sidebar header's brand pink.
- Existing Playwright tests still pass (`bun run test` in `web/dash0/`),
  particularly anything keyed off `login-logo` test-id.

## Out of scope

- Customer-facing public status page (`web/status0`) — see spec `03`.
- Per-org / per-tenant brand customization (the org may want to override
  `--brand` with their own color on their public status page — a separate
  feature).
- Email template HTML (separate spec).
