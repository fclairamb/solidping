---
model: sonnet
effort: medium
---

# The badge embed snippets paste an unlinked image, so a badge on a customer's site goes nowhere

## Problem

Reported from the product side on 2026-08-31, looking at a real badge embedded
on a static page: **the badge is an image, not a link.** Clicking it does
nothing.

The snippets the appearance page hands you to copy
(`/dash0/orgs/$org/status-pages/$statusPageUid/appearance`, "Status badge"
card) are built in
`web/dash0/src/components/shared/status-page-badge-card.tsx:79-80`:

```ts
const markdownCode = `![${pageName} status](${badgeUrl})`;
const htmlCode = `<img src="${badgeUrl}" alt="${pageName} status" />`;
```

Both emit a bare image. Pasted into a README, a docs page, or a footer, the
badge renders and updates correctly — and is completely inert. A status badge
whose entire purpose is "click here to see whether we are up" is the one image
on the page that should always be a link.

### This is NOT a regression of spec 2026-08-30-01

That spec ("Badge and widget previews on the appearance page should open the
status page when clicked") was implemented and is correct: the *preview*
`<img>` inside dash0 is now wrapped in an anchor
(`status-page-badge-card.tsx:131`, `data-testid="status-page-badge-preview-link"`)
and the widget preview's iframe sandbox was widened to
`allow-scripts allow-popups allow-popups-to-escape-sandbox`
(`status-page-widget-card.tsx:403`). Both work.

Its scope was the previews *inside the dashboard*. Nobody ever changed the
snippets a customer copies OUT of the dashboard, which is the surface that
actually ships to end users. The two are easy to conflate — hence this note,
so this spec is not closed as a duplicate.

## Proposal

Wrap both snippets in a link to the public status page, in
`status-page-badge-card.tsx`:

```ts
const markdownCode = `[![${pageName} status](${badgeUrl})](${pageUrl})`;
const htmlCode =
  `<a href="${pageUrl}"><img src="${badgeUrl}" alt="${pageName} status" /></a>`;
```

Details that matter:

1. **`pageUrl` must be ABSOLUTE**, like `badgeUrl` already is
   (`${window.location.origin}${path}`, line 77). A relative href is useless in
   a README on another host — which is the whole point of the snippet. Derive
   it the same way, from the same origin, so the two URLs in one snippet can
   never disagree about scheme or host.
2. **Point at the public status page path** the rest of dash0 links to —
   `/status0/${org}/${pageSlug}` (see
   `status-pages.$statusPageUid.appearance.tsx:228`,
   `status-pages.index.tsx:112`, and the preview anchor added by
   2026-08-30-01 at `status-page-badge-card.tsx:131`). Reuse that same
   derivation rather than composing a second one.
3. **The HTML snippet is pasted into other people's pages** — it is a
   cross-origin outbound link, so it should carry
   `rel="noopener noreferrer"` and, matching the rest of dash0's outbound
   status-page links, `target="_blank"`. Markdown has no equivalent; that is
   fine and expected.
4. **Escape what goes into the snippet.** `pageName` is operator-controlled
   free text and lands inside an HTML `alt=""` attribute and inside Markdown
   `[]()` brackets. A page named `Acme "Prod"` or `All ] status` currently
   produces a broken snippet. At minimum escape `"` for the HTML `alt`, and
   `[`/`]` for the Markdown alt text. This is a correctness bug in the
   existing code that this change makes more visible, not a new one.

## Acceptance

- The Markdown snippet is `[![...](badge)](page)` and the HTML snippet wraps
  the `<img>` in an `<a href>` with `rel="noopener noreferrer"`.
- Both URLs in a snippet are absolute and share one origin.
- A page whose name contains `"` or `]` still yields a snippet that parses.
- Unit or E2E coverage asserts the snippet TEXT, not just that a card
  rendered — the bug is the exact string a user copies, so the test has to
  read that string. `web/dash0/e2e/` already exercises this card via
  `badge-embed-markdown` / `badge-embed-html` testids
  (`status-page-badge-card.tsx:163`), which is the cheapest place to pin it.
- The custom `label` and `style` controls still flow into `badgeUrl`
  unchanged — the link wraps the existing URL, it does not replace it.

## Out of scope

The live status widget (`status-page-widget-card.tsx`) already emits its own
anchor when linking is enabled; it needs no change here.
