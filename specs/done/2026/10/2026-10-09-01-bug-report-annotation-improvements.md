---
model: sonnet
effort: medium
---

# Bug report screenshot and annotation is limited: no mobile annotation, `window.prompt` for text, charts missing, few tools

## Problem
The dashboard bug report (`web/dash0/src/components/feedback/`) already captures a screenshot with `html-to-image` and offers rect / arrow / text annotations. Gaps:

- Annotation is desktop only: the toolbar and canvas sit in a `hidden sm:block` wrapper (`FeedbackDialog.tsx`), so on mobile the user can't mark anything. Every page must be usable on mobile.
- The text tool uses `window.prompt("Annotation text")` (`AnnotationCanvas.tsx`): blocking, not translated, ugly, and unusable in some embedded browsers.
- Charts are missing from the screenshot: `useFeedback.ts` filters out every `<canvas>` node, so the part of the page the user most often reports on is blank.
- Tools are minimal: no freehand pen, no highlight, no blur/redact (a screenshot of a dashboard can contain emails, tokens, check URLs), no redo, no color choice, and the `select` tool does nothing (no move/delete of an existing annotation).
- The screenshot captures the whole `document.documentElement`, so the image can be much taller than the viewport and the annotation area gets tiny.
- `types.ts` still says the types are "reserved for a future spec, not used in v1", which is stale.

## Proposal
1. Mobile: remove the `hidden sm:block` on the annotation block in `FeedbackDialog.tsx`. Make the toolbar wrap, give buttons a 40px touch target, and make the canvas use pointer events with `touch-action: none` so drawing doesn't scroll the dialog. Let the dialog scroll when the preview is tall.
2. Text tool: replace `window.prompt` in `AnnotationCanvas.tsx` with an inline `<input>` positioned over the click point (commit on Enter or blur, cancel on Escape). Use an i18n key in the `feedback` namespace for the placeholder (all locales, see `web/dash0/src/locales/`).
3. Charts: replace the blanket canvas filter in `useFeedback.ts` by cloning each `<canvas>` to an `<img>` via `canvas.toDataURL()` before capture (and restoring after), falling back to skipping a canvas only if `toDataURL` throws (tainted canvas).
4. New tools in `AnnotationToolbar.tsx`, `AnnotationCanvas.tsx` and `types.ts`: `pen` (freehand), `highlight` (semi-transparent filled rect), `blur` (pixelate the region in `renderAnnotations`, so the redacted pixels never leave the browser). Add redo next to undo.
5. Color: a small palette (red default, plus 3 more) applied to new annotations; the `color` field already exists on `BaseAnnotation`.
6. `select` tool: click an annotation to select it, `Delete`/`Backspace` removes it, drag moves it.
7. Capture area: crop the screenshot to the visible viewport by default (use `windowWidth/Height`-sized capture or crop after `toBlob`), keeping the current full-page capture reachable only if the open question below says so.
8. Update the stale comment at the top of `types.ts` and document the shortcut and tools in `web/docs/` if the bug report is documented there (grep `Ctrl+Shift+B`).
9. Add the new toolbar buttons to the design reference page only if a new shared primitive is introduced (`web/dash0/src/routes/orgs/$org/design-reference.tsx`); keep everything inside `components/feedback/` otherwise.

## Tests
- `web/dash0/e2e/bug-report.spec.ts`: on a mobile viewport, the annotation toolbar is visible, drawing a rect with touch/pointer events and submitting posts a screenshot that differs from the un-annotated one.
- Same file: the text tool opens an inline input (no `dialog` event is fired by the page, i.e. no `window.prompt`), Enter commits, Escape cancels and adds nothing (negative case).
- Same file: pen, highlight and blur each add one annotation; undo then redo restores it; with zero annotations undo and redo are disabled.
- Same file: select an annotation, press Delete, it disappears; Delete with nothing selected does nothing.
- Unit test (vitest if dash0 has it, otherwise Playwright component-level) for `renderAnnotations` blur: pixels inside the region differ from the source, pixels outside are identical.
- Charts: on a page with a canvas chart, the captured blob is non-blank in the chart area, and a canvas whose `toDataURL` throws doesn't fail the capture (the dialog still opens).
- Backend `server/internal/handlers/feedback` tests: unchanged contract, run them to confirm nothing regressed.

## To verify
- Whether `web/docs/` documents the bug report and shortcut (grep for `Ctrl+Shift+B` / "bug report").
- Whether dash0 has a vitest setup for the pixel-level blur test.
- That `html-to-image` still renders correctly after canvas-to-img swapping (cleanup of the swapped nodes in a `finally`).

## Open questions
- Scope: all of the above in one change, or only the mobile fix, the `window.prompt` replacement and the chart capture first? Recommended: ship items 1 to 3 and 8 first, then tools (4 to 6) in the same spec if time allows, since they are independent.
- Viewport crop (item 7): crop to the visible viewport by default, or keep the full page? Recommended: viewport only, since the annotated area is what the user was looking at and the image stays small.
- Should the user be able to pick any region of another window/tab via `getDisplayMedia`? Recommended: no, it needs a permission prompt, doesn't work on mobile, and in-page capture is enough.

## Resolved open questions
- Scope: implement all items in this one spec. Do items 1 to 3 and 8 first, then the tools (4 to 6), since they are independent.
- Viewport crop (item 7): crop to the visible viewport by default, not the full page.
- Do not use `getDisplayMedia`. Keep in-page capture only.
