---
model: sonnet
effort: medium
---

# Make transactional emails readable in Gmail Android dark mode: own every link color, full-bleed on phones, flat CSS

GitHub Issue: https://github.com/fclairamb/solidping/issues/492

## Source

The owner reports `membership_request_new` (and by extension every template, since the cause
is the shared layout) rendering badly in the Gmail Android app with the app in dark mode, at
phone width:

- a full-brightness white card inside the dark Gmail UI;
- light-blue, near-unreadable text on that white card: the requester's email address and the
  fallback URL in the "Or paste this link into your browser" box. Neither is an `<a>` in the
  template. Gmail auto-links them and paints them with its own dark-mode link color;
- a card that wastes width on a phone, with the monospace fallback URL breaking mid-word;
- a thin dark seam above the navy header (rounded corners over the dark Gmail background).

The report is right on every point; the diagnosis below confirms it in the code.

## Problem

- **The light pin.** `server/internal/email/templates/base.html:32-33` and `:49` set
  `color-scheme: light only`. Gmail honours it (the card stays white) but Gmail does not
  support `@media (prefers-color-scheme)` (`wiki/features/email-dark-mode.md:25`), so the
  designed dark palette (`base.html:185-227`) never reaches it.
- **Auto-linked values with no author color.** `base.html:10` asks clients not to auto-detect
  URLs and emails (`format-detection ... email=no,url=no`), and Gmail ignores it. Values are
  printed as plain text:
  - `membership_request_new.html:7`: `({{.RequesterEmail}})`;
  - the fallback URL `<p class="fallback">{{.X}}</p>` in 9 templates: `invitation.html`,
    `membership_request_new.html`, `membership_request_decision.html`, `password-reset.html`,
    `password-reset-sso.html`, `registration.html`, `status-subscriber-confirm.html`,
    `welcome.html` (all after "Or paste this link into your browser:"), plus `.mono`
    values in alert templates.

  Gmail's auto-links take its dark-theme link color (about `#8ab4f8`) on our white card.
  An explicit `<a>` with an inline `color` keeps the author's color under the pin.
- **The fallback block.** `.fallback` (`base.html:150`) is a bordered, monospace box with
  `word-break: break-all`, which splits URLs mid-word on a 360px viewport.
- **Phone width.** `.wrapper` pads `32px 12px` (`base.html:57`), and `.container` adds a 1px
  border, a 14px radius, `overflow: hidden` and a three-layer shadow (`base.html:58`). The
  `@media (max-width: 480px)` block (`base.html:228-244`) shrinks inner paddings but keeps all
  of those, so the card never goes full-bleed.
- **Decorative CSS Gmail drops anyway.** `box-shadow` and `background-image` gradients on
  light surfaces (`.quote` `:99`, `.btn-secondary td` `:111`, `.details-table td.label`
  `:125`, `.metric` `:135`, `.footer` `:157`, the container shadow `:58`) render differently
  in Gmail and desktop clients for no benefit. Their solid `background-color` fallbacks already
  exist (enforced by `TestDarkPalette_EveryGradientKeepsASolidFallback`,
  `server/internal/handlers/emailpreview/rendering_test.go:203`).

## Why a spec

The fix plus its tests span `base.html`, about 10 templates, the rendering tests and the wiki
device matrix (more than 4 source files). The dark-mode pin itself is an open, human-gated
decision (`wiki/features/email-dark-mode.md:113`, "The Gmail decision — open, human-gated").

## Proposal

1. **Own every link color.**
   - Add a `link` template helper (or partial) in `base.html` that renders
     `<a href="..." style="color:#1e64ef;text-decoration:underline;">text</a>`, inline style
     (Gmail strips some `<style>` rules in its app; inline survives).
   - Requester email in `membership_request_new.html:7` becomes a `mailto:` link through it.
   - Every fallback URL becomes a link through it.
   - Values that must not be links (hostnames, IPs, check targets in `.mono`) are kept from
     auto-detection by inserting a zero-width joiner (`&#8205;`) after `@`, `://` and `.`. Do
     it in a template func (`nolink`), not by hand per template.
2. **Simplify the fallback block.** Replace `.fallback` with a plain small paragraph
   (`font-size: 13px; color: #475569; word-break: break-word; overflow-wrap: anywhere;`), no
   border, no monospace, the URL as a link (step 1). Keep it on mobile: some clients do not
   render the button.
3. **Full-bleed on phones.** Under `max-width: 480px`: `.wrapper { padding: 0; }` and
   `.container { border: 0; border-radius: 0; box-shadow: none; }`. `.content` keeps its 20px
   side padding. This also removes the seam above the header.
4. **Flatten decorative CSS on light surfaces.** Drop `box-shadow` and the light-surface
   gradients listed above, keeping their solid `background-color`. Reduce the container
   radius to 8px. Keep the saturated chrome gradients (header, accent bar, status banners,
   primary button) as they are: `TestDarkPalette_LeavesTheSaturatedChromeAlone` (`:232`)
   guards them, and they degrade to their solid fallback in Gmail.
5. **Do not flip the pin in this spec.** Leave `color-scheme: light only`. Add a dated note to
   `wiki/features/email-dark-mode.md` (Gmail decision section) recording this report as a
   data point. The device matrix rows (`:199-202`) need a human with a phone. Leave them for
   the owner and say so in the PR.

### Tests

- New test next to `TestPreview_PinsLightRendering`
  (`server/internal/handlers/emailpreview/rendering_test.go:121`): render
  `membership_request_new`, `invitation`, `password-reset` and `welcome`, and assert that the
  fallback URL and the requester email appear only inside an `<a ... style="color:#1e64ef`
  and never as bare text.
- A test that `.fallback` (or its replacement) no longer uses `break-all` or a monospace font.
- A test that the `max-width: 480px` block zeroes `.wrapper` padding and the container
  border/radius/shadow.
- Existing `TestPreview_PinsLightRendering` and `TestDarkPalette_*` stay green.

## Closing the issue

This spec closes #492. The implementing PR body **must** carry one `Closes #492` line so the
squash-merge closes it. When the spec is archived to `specs/done/`, verify the issue is
closed and, if the merge did not close it, close it by hand:

    gh issue close 492 --comment "Implemented by <PR or commit>; spec: <archived spec path>"
