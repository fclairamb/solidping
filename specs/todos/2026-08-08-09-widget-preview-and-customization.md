---
model: sonnet
effort: high
---

# Live status widget has no preview and exposes almost no customization

## Problem

The embed widget shipped in 2026-08-08-08 works, but the dash0 snippet
generator ([status-page-widget-card.tsx:59](web/dash0/src/components/shared/status-page-widget-card.tsx:59))
is copy-paste-blind: the user picks mode / theme / position and copies a
`<script>` tag without ever seeing what the pill will look like on their
site. To check the result they have to paste the snippet into a real page.

Customization is also thinner than what the widget already supports:

- The widget honors per-state label overrides
  (`data-label-operational|-degraded|-down|-maintenance|-unknown`,
  [widget.ts:20](web/status0/src/embed/widget.ts:20) and
  [widget.ts:238](web/status0/src/embed/widget.ts:238)) — the generator UI
  never exposes them, so in practice nobody discovers them.
- There is no size control: one pill size must fit heroes, navbars, and
  footers alike.

## Proposal

### 1. Live preview in the generator card

Extend `StatusPageWidgetCard` with a preview area that renders the **real
shipped widget** — not a React replica that would drift:

- A sandboxed `<iframe srcDoc={...}>` containing exactly the generated
  snippet (`${origin}/embed/v1/widget.js` with the currently selected
  data-attributes), so what you preview is byte-for-byte what customers
  embed. Re-render the srcDoc when any option changes.
- A **preview-state picker** (operational / degraded / down / maintenance /
  unknown, default operational) so users can see every state — including
  their custom labels — without their production status changing.
- The iframe body background follows the selected widget theme (`light` /
  `dark`; `auto` follows dash0's current theme) so contrast is judged
  honestly. Floating mode previews naturally: the pill pins to the iframe's
  corner.
- Because the preview drives the state itself (see `data-force-status`
  below), it works even before the status page is published and makes no
  summary-endpoint calls from the card.

### 2. New widget attributes (additive to the frozen `/embed/v1/` contract)

Additive attributes are backward-compatible — existing pasted snippets don't
use them — so they may land in v1. Keep the set deliberately small:

- `data-force-status="operational|degraded|down|maintenance|unknown"` —
  skip polling and render that state statically. Exists for the dash0
  preview (and docs); harmless if a customer uses it, since a static pill
  can already be faked with plain HTML. Invalid values are ignored (normal
  polling behavior).
- `data-size="sm" | "md" (default) | "lg"` — scales font/padding/icon of the
  pill; `md` must stay pixel-identical to today's rendering. Exposed as a
  Select in the generator card.
- Expose the **existing** `data-label-*` overrides in the generator UI:
  five optional text inputs (collapsed behind a "Customize labels"
  disclosure to keep the card compact), emitted into the snippet only when
  non-empty. No widget change needed for this part.

The attribute-parsing and rendering changes must preserve the widget's
security posture: `textContent`-only writes, static stylesheet, and the
constraints enforced by `TestEmbedWidgetSourceIsXSSSafe` — attribute values
(labels especially) must never reach `innerHTML`-style sinks.

### 3. Tests

- **status0 Playwright** (`web/status0/e2e/embed-widget.spec.ts`): extend —
  `data-force-status` renders each state without hitting the summary
  endpoint; `data-size` changes the computed pill dimensions; hostile label
  overrides still render as text.
- **dash0 Playwright** (`status-page-appearance.spec.ts`): the preview
  iframe shows the pill; switching preview state / theme / size updates it;
  filling a label override updates both the preview and the emitted snippet.
- **Go**: no server change expected (same `/embed/v1/widget.js` route); the
  existing `TestEmbedWidgetJS` / `TestEmbedWidgetSourceIsXSSSafe` must stay
  green.
- i18n for all new card strings in en / fr / de / es, and update the design
  reference if a new disclosure/preview pattern is introduced.

### Out of scope

- Color/accent overrides and white-labeling (that's the paid entitlement
  hook noted in spec 08 — leave it untouched).
- A dot-only / icon-only compact variant.
- `/embed/v2/`: nothing here should require breaking the v1 contract.

### Open questions

- Whether `data-force-status` should be documented in the public embed docs
  or left as an internal preview affordance — implementer's call; a one-line
  doc mention is fine either way.

## Resolved open questions

**Q: "Whether `data-force-status` should be documented in the public embed docs
or left as an internal preview affordance — implementer's call; a one-line doc
mention is fine either way."**

**Decision: implementer's call — not a blocker.** The spec already delegates
this, and both outcomes are acceptable, so pick one and move on. Either document
it in one line in the embed docs alongside the other data-attributes, or leave
it undocumented as an internal preview affordance. Do not stop or escalate over
this choice; state which you chose in your final report.
