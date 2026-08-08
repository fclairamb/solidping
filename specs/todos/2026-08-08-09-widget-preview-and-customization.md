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

## Implementation Plan

1. **`web/status0/src/embed/widget.ts`** (additive to the frozen v1 contract):
   - `data-force-status="operational|degraded|down|maintenance|unknown"`:
     parsed in `boot()` before any network access; when valid, render that
     status statically (label from `readLabels`, no title, no href) and
     `return` — no `fetch`, no `setInterval`. Invalid/absent falls through to
     the existing polling path unchanged.
   - `data-size="sm"|"md"(default)|"lg"`: a `sizeClass()` helper mirroring
     `themeClass()`; `"md"` maps to `""` (no extra class) so the existing
     `.sp-pill`/`.sp-dot` rules stay untouched — pixel-identical by
     construction. `sm`/`lg` add `.sp-size-sm`/`.sp-size-lg` root classes with
     new scoped CSS rules for padding/font-size/gap/dot-size.
   - Update the file's header doc comment to list both new attributes and note
     they're additive (no `/embed/v2/` needed).
2. **`web/status0/e2e/embed-widget.spec.ts`**: add cases — each `data-force-status`
   value renders without any summary-endpoint request; `data-size="sm"|"lg"`
   changes computed pill padding/font-size while default (`md`, and an
   explicit unrecognized value) matches today's computed style; a hostile
   label survives as text under `data-force-status` too (no network path to
   race against).
3. **dash0 `StatusPageWidgetCard`** (`web/dash0/src/components/shared/status-page-widget-card.tsx`):
   - New `size` state (Select, default `md`) emitted as `data-size` only when
     non-default.
   - New `labels: Record<PageStatus, string>` state (5 inputs) behind a
     `CollapsibleSection` ("Customize labels"); each non-empty value emits
     `data-label-<status>` in the snippet, HTML-attribute-escaped (`"`, `&`,
     `<`, `>`) so a value containing a quote can't break the tag customers
     paste onto their own site.
   - New `previewStatus` state (Select, default `operational`) driving a
     sandboxed `<iframe sandbox="allow-scripts" srcDoc={...}>` preview: the
     srcDoc embeds the exact script tag (all current attributes) plus
     `data-force-status={previewStatus}`, so the preview never calls the
     summary endpoint and works pre-publish. Body background follows the
     selected widget theme (`light`/`dark` literal, `auto` mirrors dash0's
     own `html.dark` class via a small `MutationObserver`-backed hook).
   - Real (copied) snippet stays force-status-free — only the preview iframe
     gets the extra attribute.
4. **i18n**: add `widget.size`, `widget.sizes.{sm,md,lg}`,
   `widget.previewTitle`, `widget.previewState`, `widget.previewStates.*`,
   `widget.customizeLabels`, `widget.labelOverrides.*`,
   `widget.labelPlaceholders.*` to `statusPages.json` in en/fr/de/es.
5. **Design reference**: add a "Sandboxed preview (iframe)" pattern section
   documenting the `srcDoc` + escape-then-interpolate + `sandbox="allow-scripts"`
   approach, since this is a new reusable pattern (distinct from the existing
   `src=`+`postMessage` custom-CSS preview iframe).
6. **dash0 `status-page-appearance.spec.ts`**: extend the widget test — preview
   iframe shows the pill by default; switching preview state/theme/size updates
   the rendered pill; filling a label override updates both the preview and the
   copyable snippet; a hostile label (quote-breakout attempt) still renders as
   inert text inside the preview and appears escaped in the copied snippet.
7. **Docs**: add `data-size` to the attribute table in
   `web/docs/docs/features/status-pages.md` and the `openapi.yaml`
   `getEmbedWidgetV1` description. `data-force-status` is documented too (one
   line, framed as a preview/testing affordance) — see final report for the
   "implementer's call" rationale.
8. **Go**: no server-side code changes expected; only run
   `TestEmbedWidgetJS` / `TestEmbedWidgetSourceIsXSSSafe` as a regression gate
   because `widget.ts` changed.
