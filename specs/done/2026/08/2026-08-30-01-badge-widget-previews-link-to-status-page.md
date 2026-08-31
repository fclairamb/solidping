---
model: sonnet
effort: medium
---

# Badge and widget previews on the appearance page should open the status page when clicked

## Problem

On the status page appearance screen
(`/dash0/orgs/$org/status-pages/$statusPageUid/appearance`), the "Status badge"
and "Live status widget" cards show previews of what customers will embed, but
clicking those previews goes nowhere:

- **Badge preview** — a plain `<img>` with no surrounding link
  (`web/dash0/src/components/shared/status-page-badge-card.tsx:125`). The badge
  is precisely the thing customers embed *as a link to their status page*
  (the card's own markdown snippet is `![...](badgeUrl)` and the HTML snippet
  an `<img>`), yet the preview itself is inert.
- **Widget preview** — an iframe rendering the real `/embed/v1/widget.js`
  (`web/dash0/src/components/shared/status-page-widget-card.tsx:399`). The
  widget itself *does* render its pill as an
  `<a target="_blank" rel="noopener noreferrer">` pointing at the status page
  when linking is enabled (`web/status0/src/embed/widget.ts:387` area), but the
  preview iframe is sandboxed with `sandbox="allow-scripts"` only
  (`status-page-widget-card.tsx:403`), so the click on the pill is silently
  swallowed by the sandbox — no popup permission, no navigation, nothing.

Both previews look interactive and represent link-bearing embeds, so a dead
click is a small but real WTF.

## Proposal

Make both previews open the public status page in a new tab, matching the rest
of dash0 which links to `/status0/${org}/${page.slug}` with
`target="_blank" rel="noopener noreferrer"` (e.g.
`status-pages.$statusPageUid.appearance.tsx:228`,
`status-pages.index.tsx:112`).

1. **Badge card** (`status-page-badge-card.tsx`): wrap the preview `<img>` in
   an `<a href={`/status0/${org}/${pageSlug}`} target="_blank"
   rel="noopener noreferrer">`. Keep the existing
   `data-testid="status-page-badge-preview"` reachable (on the img is fine; the
   anchor can get its own testid, e.g. `status-page-badge-preview-link`).
   Optionally add a title/aria-label ("Open status page") for accessibility —
   translated via the existing `badges` / `statusPages` namespaces (all
   locales).

2. **Widget card** (`status-page-widget-card.tsx`): let the widget's own
   anchor work inside the preview by extending the iframe sandbox to
   `sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox"`.
   The widget already targets `_blank` and derives its href from the summary
   API's `page.url` through `safeHref()` (http/https only,
   `web/status0/src/embed/widget.ts:230`), so no new attack surface is opened —
   the popup escape only applies to that already-validated URL. Note the pill
   only becomes an `<a>` when `linkEnabled` is on (the card's "link" checkbox,
   `status-page-widget-card.tsx:160`) and the summary returns a usable
   `page.url`; when the user unchecks linking, the preview correctly stays a
   non-clickable `<span>` — that behavior must not change, since the preview's
   job is to mirror the real embed.

3. **Tests**: extend the existing appearance-page E2E coverage
   (`web/dash0/e2e/` — the suite that exercises
   `status-page-badge-card` / `status-page-widget-preview` testids) to assert:
   - the badge preview is wrapped in an anchor whose `href` ends with
     `/status0/{org}/{slug}` and has `target="_blank"`;
   - the widget preview iframe's `sandbox` attribute includes `allow-popups`;
   - (if cheap) inside the preview iframe, the pill is an `<a>` when the link
     checkbox is on and a `<span>` when it's off.

Out of scope: changing the embedded widget script itself (`/embed/v1/widget.js`
is a frozen public contract and already does the right thing), and the
custom-domain URL question — the dash0-internal `/status0/...` path is what
every other "view status page" link uses.
