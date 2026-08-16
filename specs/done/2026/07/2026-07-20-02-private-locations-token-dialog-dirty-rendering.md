---
model: sonnet
effort: high
---

# Enrollment-token dialog on the private-locations page renders dirty — UI fragments bleed outside the dialog and over the backdrop

## Problem

On `/dash0/orgs/$org/organization/private-locations` (observed on the k8xp dev
deploy, `https://solidping.k8xp.com/dash0/orgs/acmetech/organization/private-locations`),
opening the **Enrollment token** dialog (minted via the `KeyRound` row action)
produces visibly broken rendering. A screenshot shows:

- The dialog itself (title, description, token `<code>` field, copy button,
  "Run the agent with:" `<pre>` docker block) is centered and mostly correct.
- **Bright, un-dimmed UI fragments render outside the dialog's rounded
  boundary**, on top of the `bg-black/80` backdrop: a rounded strip containing
  a `0` badge at the top right (the region row's Agents count area), plus
  what look like offset duplicates/extensions of the token row and the docker
  `<pre>` block stretching past the dialog's right edge toward the viewport
  edge. Each fragment has its own rounded outline, giving a "torn" look.
- The dialog description text ("…enrolls exactly one agent.") visibly runs
  *across* one of the bright fragments, i.e. the fragment paints between the
  backdrop and the dialog text.

Relevant code:

- The dialog: `TokenRevealDialog` in
  `web/dash0/src/routes/orgs/$org/organization.private-locations.index.tsx:306-344`
  — token row (`flex-1 overflow-x-auto` `<code>` + copy button) at lines
  320-330, docker command `<pre className="overflow-x-auto">` at lines 334-340.
  Both hold long unbreakable strings (64-hex `spe_…` token, `docker run`
  command), which are exactly the elements that appear to bleed.
- The primitive: `web/dash0/src/components/ui/dialog.tsx` — standard shadcn
  portal with `z-50` overlay (line 21) and `z-50 grid w-full max-w-lg`
  content (line 38). A quick grep found no page element using `z-50`/higher
  outside the overlay primitives, so a plain z-index war is not the obvious
  culprit.

Root cause is **not yet established**. Plausible hypotheses to check:

1. Content overflow: the long token / docker command widening the dialog's
   grid track (missing `min-w-0` on grid/flex children) so children paint
   past `max-w-lg` and outside the rounded border.
2. A stacking-context leak letting page elements (the Agents `Badge`, table
   cells) paint above the overlay — e.g. a transform/filter/sticky ancestor
   created by the page or by the dialog's `zoom-in`/`slide-in` animation.
3. A stuck open/close animation (`tailwindcss-animate` `data-[state=…]`
   classes) leaving ghost frames.
4. Environment skew: the k8xp dev pods may run an older image — confirm the
   bug reproduces on current `main` locally before fixing.

## Proposal

1. **Reproduce first.** Locally (`make dev-test`), create a private region,
   mint an enrollment token, and open the dialog. Try realistic conditions:
   default and narrow viewports, light/dark, and text selection inside the
   `<pre>` (the screenshot shows selected text). Use Playwright or the
   browser to capture the broken state; if it will not reproduce on current
   `main`, verify against the deployed k8xp image tag and record the finding
   in the spec before closing it as environment skew.
2. **Diagnose and fix** whichever hypothesis holds. Likely fixes, keep it
   minimal:
   - Constrain overflow inside the dialog: `min-w-0`/`max-w-full` on the
     token row and `<pre>` so long strings scroll inside the dialog instead
     of blowing out its box (this also matters for the mobile-usability rule
     — the dialog must stay usable on small screens).
   - If it is a stacking issue, fix it at the primitive level in
     `web/dash0/src/components/ui/dialog.tsx` so every dialog benefits, and
     mirror the change in `alert-dialog.tsx` if it shares the flaw.
3. **Regression coverage.** Add a dash0 E2E assertion (in the existing
   private-locations spec file under `web/dash0/e2e/`) that with the token
   dialog open, the token `<code>` and `<pre>` bounding boxes stay within the
   `DialogContent` bounding box (no horizontal bleed), at desktop and mobile
   viewport widths.
4. If the fix changes the shared dialog primitive, sanity-check the
   design-reference page (`web/dash0/src/routes/orgs/$org/design-reference.tsx`)
   dialog examples still render correctly.

## Open questions

- Exact browser/OS where the screenshot was taken (fragments could be
  compositing artifacts specific to one browser); if it cannot be reproduced
  in Chromium/WebKit under Playwright, ask before closing.

## Implementation Plan

1. **Reproduce locally.** Use the running `make dev`/`make dev-test` backend
   (default org, `admin@solidping.com` / `solidpass`), create a private
   region via the page (or API for speed), mint an enrollment token, open
   the `TokenRevealDialog`, and measure `getBoundingClientRect()` for
   `[role="dialog"]`, the `[data-testid="minted-token"]` `<code>`, and the
   `<pre>` docker block via the browser tool's JS eval. Confirm the `<code>`
   and `<pre>` right edges exceed the dialog's own right edge (this is the
   "bleed").
2. **Diagnose.** `DialogContent` is `display: grid` with no explicit
   `grid-template-columns`. Its direct children (here, the wrapper
   `<div className="space-y-3">` holding the token row and `<pre>`) are grid
   items whose automatic minimum size defaults to their **min-content**
   size when the item itself has `overflow: visible` (the CSS Grid/Flexbox
   "automatic minimum size" carve-out that zeroes this out only applies to
   overflow set on the item itself, not on a descendant several levels
   down). Since the docker command / hex token render in `white-space: pre`
   elements with no wrap opportunity, their min-content width is huge, so
   the wrapper div's min-content width — and hence the grid track — grows
   past `max-w-lg`. `DialogContent` has no `overflow-hidden`, so the
   oversized content paints past the dialog's visible rounded/bordered box,
   each still carrying its own `rounded bg-muted` background — exactly the
   "torn fragment" look reported. This is hypothesis 1 from the Problem
   section, confirmed by measurement (not hypotheses 2–4: no stacking-context
   leak, no stuck animation, and it reproduces on current `main` locally, so
   not environment skew).
3. **Fix, at both levels (belt-and-suspenders):**
   - Primitive level (`web/dash0/src/components/ui/dialog.tsx`): add
     `[&>*]:min-w-0` to `DialogContent`'s className so every direct child of
     any dialog gets `min-width: 0`, preventing this class of grid blowout
     for all current and future dialogs, regardless of what a consumer
     renders inside.
   - Mirror the identical one-line change in
     `web/dash0/src/components/ui/alert-dialog.tsx`'s `AlertDialogContent`
     (same `grid w-full max-w-lg` structure, same latent flaw, even though
     no current `AlertDialogContent` usage has long unbreakable content
     today).
   - Local defense-in-depth in
     `organization.private-locations.index.tsx`: add `min-w-0` to the
     token-content wrapper div and the token-row flex container, and
     `max-w-full` to the `<pre>`, matching the spec's own suggested fix so
     the usage site stays correct even if a future refactor bypasses the
     shared primitive.
4. **Regression coverage.** Add an assertion to
   `web/dash0/e2e/private-locations.spec.ts` (existing "mint one-shot token"
   test) that the token `<code>` and docker `<pre>` bounding boxes stay
   within `[role="dialog"]`'s bounding box, both at default desktop width
   and at a narrow (mobile) viewport width.
5. **Design-reference sanity check.** Since the shared primitive changed,
   manually verify `design-reference.tsx`'s dialog and alert-dialog examples
   still render correctly (no visual regression from `[&>*]:min-w-0`).
6. `make fmt`, then scoped QA: `make build-dash0`, `bun run lint` (no new
   errors in touched files).
