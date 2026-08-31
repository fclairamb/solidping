# Email dark mode

How SolidPing's transactional email renders in a dark-mode inbox: what we
designed, what we deliberately did not, and the one decision still left to a
human with a phone.

Source of truth: `server/internal/email/templates/base.html`. Every one of the
24 shipped templates styles itself exclusively through that file's classes, so
everything below is a property of one stylesheet.

## The problem

An escalation email read at 3am on a dark-mode phone used to render as a
full-brightness white card in an otherwise dark inbox. That was the *designed*
outcome, not an accident — see the light pin below — but it is a genuinely bad
reading experience for exactly the mail that gets read at 3am.

## What each client actually does

| Client | `@media (prefers-color-scheme: dark)` in `<style>` | Behaviour today |
|---|---|---|
| Apple Mail (iOS / macOS) | **supported** | Gets the designed dark palette |
| Outlook for Mac / iOS | **supported** | Gets the designed dark palette |
| Thunderbird | **supported** | Gets the designed dark palette |
| Gmail (Android, iOS, web) | **not supported** | Stays on the designed light rendering (the pin) |
| Outlook.com (web) | partial / rewrites CSS | Light, plus its own transform |
| Outlook for Windows (Word engine) | **ignores media queries outright** | Light, plus forced inversion — out of our control |

Two consequences follow, and they shape everything else:

1. A palette *we* design can only reach the media-query clients.
2. Gmail's only two states are "pinned light" (today) or "Gmail's own
   auto-darkening algorithm". There is no third option where we hand Gmail a
   palette.

## The light pin — what it is and why it stays

Three declarations, which are **one declaration in three places**:

- `<meta name="color-scheme" content="light only">`
- `<meta name="supported-color-schemes" content="light only">`
- `:root { color-scheme: light only; supported-color-schemes: light only; }`

They opt the mail out of the forced auto-inversion that Apple Mail,
Outlook.com and Gmail's Android app apply to a light-only email — inversion
that turns the white card grey, washes out the navy header and, worst,
recolors the status banner that carries the entire meaning of an incident
alert.

The pin **stays** even though the stylesheet now ships a designed dark
palette. The two are not in conflict: `prefers-color-scheme` reflects the
OS/app setting, which the capable clients evaluate regardless of the pin,
while Gmail honours the pin and stays on the designed light rendering rather
than auto-darkening. Worst case, a client honours the pin so strictly that the
dark block never fires — which is exactly the rendering we shipped before.
Adding the dark block is therefore strictly non-regressive.

> Honesty note: "the media block fires despite the pin" is folklore-grade
> received wisdom. It is plausible and it is what the design assumes, but it is
> **not verified** — that is precisely what the device matrix below is for. The
> design is safe either way, because the failure mode is "no change".

Change all three together or none. A partial flip leaves clients disagreeing
about the same message.

## The designed dark palette

`base.html` carries a `@media (prefers-color-scheme: dark)` block at the bottom
of its `<style>` element. It overrides only the near-white surfaces and the
dark text on them:

- page background and `.container` card → deep slate (`#0d151d` / `#141e28`)
- `.content` text, headings and links
- `.details-table` borders, the grey `.label` column, `.value` text, `th`
- `.metric` surface and its state-coloured figure (lightened, not re-hued)
- `.badge-*` pastels → dark tints of the same hue with light text
- `.btn-secondary` (the one white button) → dark surface
- `.quote`, `.fallback`, `.mono`, `.meta`, `.footnote`, `.footer`

Deliberately **left alone**, and pinned by
`TestDarkPalette_LeavesTheSaturatedChromeAlone`:

- `.header` — already dark navy. Org logos already sit on a dark surface, so
  dark logo variants are not needed.
- `.accent-bar` and every `.status-*` banner — saturated mid-tones that read
  correctly on dark. Recolouring the banner is the exact damage the pin exists
  to prevent.
- `.btn-primary` / `.btn-success` / `.cta` — saturated, legible on both.

Two rules the block inherits from the file:

- **Every `background-image` gradient keeps its paired `background-color`.**
  Outlook's Word engine ignores `background-image` outright; a gradient with no
  solid fallback renders as nothing. Enforced by
  `TestDarkPalette_EveryGradientKeepsASolidFallback`, which reads `base.html`
  itself — the older inline-style gradient test cannot see media-block rules,
  because premailer never inlines them.
- Premailer leaves media blocks in a `<style>` element and appends
  `!important` to their declarations, which is what lets them beat the inlined
  light values. `TestPreview_ShipsADarkPalette` asserts this on the rendered
  output, so a premailer upgrade that started dropping media blocks fails
  loudly instead of silently shipping light-only mail again.

### Previewing it

`/dash0/orgs/:org/test/emails` (test mode only) has a Light/Dark toggle beside
the HTML/Text one. An `<iframe>` cannot be told to report
`prefers-color-scheme: dark` to the document it hosts, so the *endpoint* does
it: `GET /api/mgmt/email-preview/:template?colorScheme=dark` rewrites the
template's own media query to `@media all`. The preview is therefore the
shipped CSS made unconditional — never a second palette that can drift.

## The Gmail decision — open, human-gated

**Not decided here. Do not flip it as a drive-by.**

### What the flip is

Change all three light-pin declarations from `light only` to `light dark`:

```html
<meta name="color-scheme" content="light dark">
<meta name="supported-color-schemes" content="light dark">
```
```css
:root { color-scheme: light dark; supported-color-schemes: light dark; }
```

That is the entire change. It does not add a single style — it *removes* the
opt-out, handing Gmail (and Outlook.com) permission to run their own
auto-darkening transform over the light design.

### What it would gain

Gmail Android/iOS/web users in dark mode would get a dark-ish message instead
of a white card. That is the majority of consumer inboxes, and the media-query
palette cannot reach any of them.

### What it risks

The algorithm mostly flips near-white backgrounds and near-black text, so the
design is reasonably well positioned: the header is already dark and the status
banners are saturated. The fragile pieces are the same ones the designed
palette had to handle:

| Element | Under the light pin | Expected under Gmail's transform | Risk |
|---|---|---|---|
| `.status-*` banner | exact brand colour | possibly re-tinted or contrast-shifted | **high** — the banner *is* the alert's meaning |
| `.header` navy | as designed | already dark; may be left alone or lightened | medium |
| `.badge-*` pastels | pastel + dark text | pastel may survive while text darkens → unreadable | medium |
| `.btn-secondary` | white + dark label | white may survive while the label lightens → invisible | medium |
| `.details-table` label column | light grey | grey-on-grey collapse | low/medium |
| `.metric` figure state colours | red/green/amber | luminance-shifted | low |

A wrong call regresses **every** alert email for the largest client, which is
why this is not a code decision.

### Go / no-go criteria

Flip only if, on real devices, all of these hold:

1. The status banner keeps a recognisable, correct severity colour with legible
   text in Gmail Android **and** Gmail iOS dark mode.
2. No badge or button ends up with same-luminance text and background.
3. The result is judged better than the current pinned-light card by someone
   who reads alerts on that device.

If any fails, keep the pin — the designed palette already covers the clients we
can actually design for.

### If it is flipped

Flip all three declarations together, update the `<head>` comment in
`base.html` (it currently states the pin is deliberate), and update
`TestPreview_PinsLightRendering`, which asserts the trio and explicitly fails
on `light dark`. That test failing is the intended speed bump, not an obstacle
to route around.

## Device matrix — human follow-up, not yet run

Browser previews cannot substitute: Gmail and Apple Mail each rewrite the CSS
differently, and neither rewrite is reproducible in a desktop browser.

Send these four templates — they cover, between them, the status banner, the
details table, the metric figure, the badges and both button variants:

- `incident-created.html` (down banner + details table + both buttons)
- `incident-escalated.html` (escalated banner)
- `uptime-report.html` (metric + badges + details table)
- `invitation.html` (buttons, no banner)

Record the result. `pass` / `fail` plus a one-line note; attach screenshots to
the spec or the PR.

| Client | Theme | incident-created | incident-escalated | uptime-report | invitation |
|---|---|---|---|---|---|
| Apple Mail iOS | light | | | | |
| Apple Mail iOS | dark | | | | |
| Gmail Android | light | | | | |
| Gmail Android | dark | | | | |
| Gmail web | light | | | | |
| Gmail web | dark | | | | |
| Outlook (Windows or Outlook.com) | light | | | | |
| Outlook (Windows or Outlook.com) | dark | | | | |

Two questions the matrix answers:

1. **Does the designed dark block actually fire** in the Apple Mail rows
   despite the light pin? (If it does not, we lose nothing — we are back to
   today's rendering — but we should stop claiming it does.)
2. **What does Gmail's auto-darkening do** to the fragile elements above? Run
   the Gmail dark rows once with the pin in place (baseline: should be
   unchanged light) and once against a build with the pin flipped, to feed the
   go/no-go criteria.

## Code map

| What | Where |
|---|---|
| Light pin + dark palette | `server/internal/email/templates/base.html` |
| Preview `?colorScheme=` rewrite | `server/internal/handlers/emailpreview/handler.go` |
| Pin / palette / gradient tests | `server/internal/handlers/emailpreview/rendering_test.go` |
| `colorScheme` endpoint tests | `server/internal/handlers/emailpreview/handler_test.go` |
| Preview UI + Light/Dark toggle | `web/dash0/src/routes/orgs/$org/test.emails.tsx` |
| Preview E2E | `web/dash0/e2e/email-preview.spec.ts` |
