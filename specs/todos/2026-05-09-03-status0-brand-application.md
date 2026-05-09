# Apply brand identity to the public status page (`status0`)

## Context

`web/status0` is the **public, unauthenticated, read-only** status page
that subscribers and end users see — distinct from the operator-facing
`dash0` (covered by spec `02`). The audience and the constraints are
different:

- **Audience.** Subscribers / end users — typically a customer of one of
  *our customers*. They glance at the page once when something feels
  broken, see green or red, and leave. Average time on page: seconds.
- **Brand role.** This is the surface most likely to be the user's first
  exposure to "SolidPing" the product (right after the marketing site).
  It deserves more brand presence than the operator dashboard does — it
  is closer to a marketing surface than to an ops console.
- **Color discipline.** Operational colors still rule (green = up, red =
  down). But because the page has no charts, no destructive buttons, no
  long-running operator tasks, the "brand pink near incident red" risk is
  lower than in `dash0`. Brand can paint chrome more generously: header
  bar, hyperlinks back to solidping.io, footer accents.

The current `status0` shares the same blue-themed Tailwind token set as
`dash0` (`web/status0/src/index.css` is a near-clone of dash0's). After
spec `01` lands, both apps have `--brand` available; this spec spends it
on the public surface.

## What we are NOT doing

- **Not** building per-tenant brand override yet. SolidPing's customers
  may eventually want to slap their own logo and color on their status
  page; that's a separate feature with its own data model. This spec
  only changes the **default** look that ships out of the box.
- **Not** turning the page into a marketing replica. The hero of
  solidping.io is a saturated full-bleed gradient — that's right for a
  marketing landing, wrong for "is the service up right now." Brand
  presence is a *header bar* and accents, not a full-bleed background.
- **Not** changing the green/yellow/red semantics of the status indicators.
  Those are inviolable.

## Surfaces to update

### 1. Top header bar

Today: presumably a thin neutral bar with the org name and maybe a logo.

Change: a fixed-height (~64px) header bar with `bg-brand text-brand-foreground`.

Inside the bar:

- Left: `<Logo size={32} variant="mark" />` + the org's display name as a
  white wordmark.
- Right: optional links (subscribe, RSS feed, history) styled as
  `text-brand-foreground/80 hover:text-brand-foreground`.

This is the visual continuity hook from solidping.io. Someone arriving
from "click the SolidPing badge on a customer's website" sees the same
crimson bar they saw on the marketing site — instant brand recognition.

### 2. Subtle "Powered by SolidPing" footer

A small, centered footer:

```
Powered by [SolidPing logo + wordmark in --brand color] · solidping.io
```

`<Logo size={16} variant="wordmark" />` styled as `text-brand`, linking to
`https://www.solidping.io`. This is the only "outbound" branding on the
page and is conventional for status-page products (Statuspage, Hyperping,
Better Stack all do this).

When per-tenant brand override ships later, paying customers will be able
to remove this footer; for the default experience, it stays.

### 3. Status hero / current-state banner

The big "All systems operational" / "Some systems are experiencing
issues" / "Major outage" banner.

Change: **do not** use `--brand` here. The banner color is determined
exclusively by status:

- All up: `bg-status-ok/10 border-status-ok` with green text.
- Degraded: `bg-status-warning/10 border-status-warning` with amber text.
- Down: `bg-status-error/10 border-status-error` with red text.

The brand bar above the banner is enough to anchor identity; the banner
itself must scream status, not brand.

### 4. Hyperlinks to solidping.io

Anywhere the page links to the marketing site (footer, "About SolidPing"
copy if it exists), use `text-brand` instead of `text-primary`. This is
the one place link color is *intentionally* split:

- Internal links (e.g. "View incident history") → `text-primary` (blue).
- External brand links (e.g. "Powered by SolidPing", "Visit
  solidping.io") → `text-brand` (pink).

Different colors signal different intent — going outside vs. drilling
deeper into the same status page.

### 5. Resource / section group headers

Status pages are organized as sections (e.g. "API", "Web", "Workers"),
each containing resources. Section headers today use a neutral
`text-foreground` heading.

Change: leave it alone. Don't paint section headers brand pink — they
are content, not chrome. (Tested mentally: pink section headers next to
green/red resource indicators creates exactly the brand-vs-status
collision we are avoiding.)

### 6. Subscribe / notification dialog

If status0 has a "Subscribe to updates" button + dialog, the button
itself stays `--primary` (it's an interactive affordance). The dialog
header may show a small `<Logo size={20} />` next to the title for brand
consistency.

### 7. Favicon and `index.html`

Already covered by spec `01` (the favicon link block in
`web/status0/index.html` gets the modern set). No additional change here
beyond verifying the deployed page surfaces the new icon.

## Color usage summary for `status0`

| Surface                 | Color token       | Why |
|-------------------------|-------------------|-----|
| Top header bar          | `--brand`         | Brand anchor, marketing continuity |
| Status hero banner      | `--status-{ok/warning/error}` | Status is the message |
| Section/resource indicators | `--status-{ok/error}` | Same |
| Internal navigation links | `--primary`     | Interactive, not brand |
| Outbound brand links    | `--brand`         | Different from internal — signals "leaves the page" |
| Subscribe / CTA buttons | `--primary`      | Interactive |
| Footer "Powered by"     | `--brand`         | Conventional outbound branding |

## Wire-up checklist

- [ ] Header bar uses `bg-brand text-brand-foreground` with
      `<Logo size={32} />` and the org name.
- [ ] "Powered by SolidPing" footer with `<Logo size={16} variant="wordmark" />`
      linking to `https://www.solidping.io`.
- [ ] Outbound brand links use `text-brand`; internal nav uses
      `text-primary`.
- [ ] Status banner colors are untouched (green/yellow/red only).
- [ ] Section headers and resource rows use neutral foreground — no
      brand color in content chrome.
- [ ] Subscribe button stays `--primary`; subscribe dialog header gets a
      small `<Logo />`.
- [ ] Favicon set from spec `01` is in place.

## Verification

- `make dev-test` — visit `/status0/test` (or whatever the dev status page
  URL is), see the brand pink header bar with the SolidPing mark.
- Toggle a check to "down" via the dashboard; the public status page
  shows a red status banner that reads as red, not as a sibling of the
  brand pink header.
- Visit on mobile width — header bar collapses cleanly, "Powered by"
  footer remains visible and tappable.
- Existing `web/status0` Playwright tests (if any) still pass.
- Lighthouse run on the public page does not regress (the SVG logo +
  inline favicons should score equal or better than the missing-image
  baseline).

## Out of scope

- Per-tenant brand customization (`--brand` override per organization,
  custom logo upload, etc.) — separate feature.
- RSS / Atom feed branding (text-only output, no styling needed).
- Email subscription template branding — same separate spec as dash0
  email work.
- Status page sections layout / IA changes — this spec is paint, not
  structure.
