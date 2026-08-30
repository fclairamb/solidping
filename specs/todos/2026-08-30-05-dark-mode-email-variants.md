---
model: opus
effort: high
---

# Emails glare bright white in dark-mode inboxes — add designed dark variants where clients allow it

## Problem

An escalation email read at night on a dark-mode phone renders as a glaring
white card in a dark inbox. That is today's *designed* outcome, not a bug:
[base.html:11-19](server/internal/email/templates/base.html) pins
`color-scheme: light only` (meta + the `:root` rule at
[base.html:30](server/internal/email/templates/base.html)) as a deliberate
opt-out of the forced auto-inversion that Apple Mail, Outlook.com and Gmail's
Android app apply to light-only mail — inversion that turns the white card
grey, washes out the navy header, and recolors the status banner that carries
the whole meaning of an incident alert. The July email rework explicitly
scoped dark variants out
([2026-07-05-10 spec, non-goals](specs/done/2026/07/2026-07-05-10-email-rework-design-links-unsubscribe.md):
"the palette should merely not break in dark-mode clients").

But incident and escalation mail is *the* woken-up-at-3am email, and a
full-brightness white card is a genuinely bad reading experience there. We can
do better than the pin for the clients that support real dark styling.

Client reality, which shapes the whole design:

- **Apple Mail (iOS/macOS), Outlook for Mac/iOS, Thunderbird** honor
  `@media (prefers-color-scheme: dark)` in embedded `<style>` — we can ship a
  palette we designed.
- **Gmail (Android/iOS apps, web)** does **not** support that media query.
  Its only two behaviors are: pinned light (today), or Gmail's own
  auto-darkening algorithm. A designed dark palette cannot reach Gmail.
- **Outlook for Windows** (Word engine) ignores media queries entirely and
  does its own forced inversion regardless; it is out of our control.

So "dark mode emails" decomposes into (a) a designed dark palette for the
media-query clients, and (b) a separate, riskier decision about whether to
un-pin Gmail and accept its auto-darkening.

## Proposal

### 1. Designed dark palette in `base.html` (the safe, autonomous part)

Add a `@media (prefers-color-scheme: dark)` block to
[base.html](server/internal/email/templates/base.html) overriding the class
tokens. All 24 templates style themselves exclusively through the base
stylesheet's classes, so this is a single-file change and future templates get
dark for free. Elements needing dark values:

- `body` / `.wrapper` background (`#f0f4f8` → deep slate), `.container` card
  (`#ffffff` → dark surface, border and shadow adjusted), `.content` text
  colors including `h1`/`h2`/links.
- `.details-table` (line 103–114): borders, the light-grey `.label` column and
  its gradient, `.value` text.
- `.badge-*` pastels (lines 126–129) — light pastel backgrounds with dark
  text; need dark-surface equivalents (e.g. translucent tints with light
  text).
- `.btn-secondary` (lines 92–93) — white button with dark label; needs a dark
  surface variant. `.btn-primary` / `.btn-success` are saturated and can
  stay.
- `.quote` (line 80), `.metric` (line 116), `.fallback` (line 131), `.meta`,
  `.footnote`, and the `.footer` (line 138) light-grey band.
- Leave alone: the `.header` (already dark navy — org logos already sit on a
  dark surface, so no branding work is needed) and the `.status-banner`
  variants (lines 64–69, saturated mid-tones that read correctly on dark).

Respect the file's existing discipline: every `background-image` gradient
keeps a paired `background-color` fallback, and note in a comment that the
Word-engine Outlook never sees this block at all (it gets the light design,
as today).

**Keep the `light only` pin in place for this step** — the three declarations
(two `<meta>` tags at lines 18–19 and the `:root` rule at line 30) stay, and
must stay in sync as a trio. Rationale: `prefers-color-scheme` reflects the
OS/app setting, which several capable clients evaluate regardless of the meta
pin, while Gmail honors the pin and stays designed-light. Worst case a client
respects the pin so strictly that the media block never fires — which is
exactly today's rendering. This step is therefore strictly non-regressive:
Gmail stays as-is, capable clients gain designed dark. The claim "the media
block fires despite the pin" is folklore-grade and is precisely what the
device matrix in step 4 verifies.

### 2. Gmail strategy — an explicit, human-gated decision

Flipping the pin to `light dark` would hand Gmail's auto-darkening the email.
The design is reasonably well positioned for it (dark header, saturated
banners — the algorithm mostly flips near-white backgrounds and near-black
text), and the fragile pieces are the same ones listed in step 1. But whether
the result beats the pinned-light status quo can only be judged on a real
device, and a wrong call regresses every alert email.

The spec's deliverable here is **not** the flip: it is a short written
decision aid — what flips, what the known fragile elements look like under
Gmail's transform, and the one-line change to execute the flip later (the
meta pair + `:root` rule). Put it in `wiki/features/` (new page, e.g.
`email-dark-mode.md`) alongside the device-matrix results template from
step 4. Do not change the pin in this spec.

### 3. Dark toggle in the email preview UI

The preview page ([test.emails.tsx](web/dash0/src/routes/orgs/$org/test.emails.tsx))
renders `/api/mgmt/email-preview/{template}?format=html` into an iframe
(lines 52, 270–276). An iframe cannot emulate `prefers-color-scheme`, so make
the endpoint do it: accept `?colorScheme=dark` in
[emailpreview/handler.go](server/internal/handlers/emailpreview/handler.go)
(`Preview`, line 41) and, for the HTML format only, mechanically rewrite
`@media (prefers-color-scheme: dark)` to `@media all` before serving. That
shows exactly the CSS a dark client would apply, with zero divergence from
the shipped template. Add a Light/Dark toggle next to the existing HTML/Text
format toggle (mirror its button pattern and testids, e.g.
`email-preview-scheme-dark`), carried as a search param like `format` is.
Also darken the preview pane surround when dark is selected so the card is
judged against a dark backdrop.

### 4. Verification

Automated (in this spec):
- Backend test: preview endpoint with `colorScheme=dark` serves HTML whose
  dark block is active (`@media all` present, `prefers-color-scheme` absent),
  and without the param serves the untouched template; assert the dark block
  exists in `base.html` and that the light pin trio is intact (the negative:
  no `light dark` meta snuck in).
- Existing rendering tests keep passing for all templates
  (`emailpreview/rendering_test.go`).
- Playwright: the toggle switches the iframe URL and persists via the search
  param.

Manual (human follow-up, recorded in the wiki page from step 2): real sends
of representative templates — incident-created (down banner), escalation,
uptime-report (metric + badges + details table), invitation (buttons) — to
Gmail Android, Apple Mail iOS, Gmail web and one Outlook, each in light and
dark themes. This matrix both validates step 1 on devices and feeds the
step 2 decision. Browser previews cannot substitute: Gmail and Apple Mail
each rewrite the CSS differently.

### Out of scope

- Flipping the `light only` pin (step 2 decides *how*, a human decides *if*).
- Per-org brand colors or dark logo variants (org branding is logo/name/hide
  only, and logos render on the always-dark navy header).
- The plaintext part, and Outlook-for-Windows forced inversion.
