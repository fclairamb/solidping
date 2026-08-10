---
model: sonnet
effort: medium
---

# The embed widget should link to the status page by default, with an opt-out toggle

## Problem

On the status page appearance screen
(`/dash0/orgs/$org/status-pages/$statusPageUid/appearance`, e.g.
`https://solidping.k8xp.com/dash0/orgs/portrait/status-pages/ce76a61b-.../appearance`),
the embeddable widget card offers mode / theme / position / size / label
customization, but nothing about the pill's link behavior.

Today's actual behavior of the shipped widget
(`web/status0/src/embed/widget.ts`):

- In the live polling path, the pill renders as an `<a target="_blank">`
  pointing at the status page whenever the public summary endpoint returns a
  validated http(s) `page.url` (`widget.ts:367-377`, `widget.ts:401-417`) —
  so the widget *does* link to the status page in normal operation.
- There is **no way to turn that link off**. The v1 attribute surface
  (documented in the header comment of `widget.ts`) has no link-related
  attribute, and the dash0 snippet builder
  (`web/dash0/src/components/shared/status-page-widget-card.tsx`,
  `buildAttributes` at line 99) can only emit what v1 understands.

The desired behavior: linking to the status page is the default (as it
effectively already is), and the customization card lets the user unselect it
for embeds that shouldn't navigate away.

## Proposal

### 1. Additive v1 attribute: `data-link`

Add `data-link` to the frozen v1 contract in
`web/status0/src/embed/widget.ts`. Per the contract's own rules
(`widget.ts:12-14`) new attributes are allowed within v1 as long as they are
additive — omitting the attribute must leave existing pasted snippets
behaving exactly as before.

- `data-link` absent or any value other than `"false"` → current behavior:
  linked when the summary provides a safe URL, `<span>` fallback otherwise.
- `data-link="false"` → never link: render the non-interactive `<span>` pill
  even when `page.url` is present (reuse the existing `mount(false)` /
  `href: null` path — the span fallback already exists for the no-safe-URL
  case, so no new DOM shape is introduced).
- Document the attribute in the header comment block alongside the others.
- No security surface change: the opt-out only ever *removes* an anchor; it
  never introduces a new href source.

### 2. dash0 widget card: "Link to status page" toggle

In `web/dash0/src/components/shared/status-page-widget-card.tsx`:

- Add a checkbox/switch "Link to the status page", **checked by default**,
  following the pattern of the existing "Customize labels" checkbox
  (`data-testid="status-page-widget-customize-labels"`).
- `buildAttributes` emits `data-link="false"` only when unchecked — the
  default (checked) emits nothing, keeping the snippet minimal and identical
  to today's output.
- The preview iframe/snippet uses `data-force-status`, which already renders
  an unlinked static pill (`widget.ts:436-439`), so the preview needs no
  change — but pass the attribute through to the preview snippet anyway so
  what users copy and what they see stay generated from the same state.
- Add the translation key(s) in the `badges` namespace next to the existing
  widget card strings.

### 3. Tests

- `web/status0` widget unit/E2E coverage: with `data-link="false"` and a
  summary response that includes a valid `page.url`, the pill is a `<span>`
  (no `href`, no `target`); without the attribute the existing linked
  behavior is unchanged (regression pin on the additive guarantee).
- dash0 Playwright (`web/dash0/e2e/`): the toggle is checked by default and
  the snippet contains no `data-link`; unchecking it adds
  `data-link="false"` to the copyable snippet.
- `server/internal/app/embed_widget_test.go` already pins the served
  `/embed/v1/widget.js` asset — extend only if it asserts on the attribute
  docs/surface.
