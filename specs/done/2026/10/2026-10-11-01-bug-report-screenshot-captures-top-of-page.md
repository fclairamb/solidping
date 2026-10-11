---
model: sonnet
effort: medium
---

# Bug report screenshot shows the top of the page, not what the user is looking at

## Problem
Spec 2026-10-09-01 (item 7) asked for the bug report screenshot to be cropped to the visible viewport. On a scrolled page the result is wrong: the image is a strip taken from the top of the page.

Reproduced on 2026-10-11 with local `make dev`, demo org, `/d/orgs/demo/checks`, 1024x768 viewport, `devicePixelRatio` 2, document 1309px tall:

| `scrollY` | Expected image | Actual image | What it shows |
|---|---|---|---|
| 0 | 2048x1536 | 2048x1536 | correct |
| 500 | 2048x1536 | 2048x536 | rows at document y 500-768, bottom half of the sidebar |
| 541 (bottom) | 2048x1536 | 2048x454 | rows at document y 541-768, bottom of the sidebar |

Cause, in `web/dash0/src/components/feedback/useFeedback.ts`:

- `toBlob(document.documentElement, ...)` sizes the image from `clientWidth`/`clientHeight` (html-to-image `getImageSize`). For `<html>` that is the viewport, so the capture is the first viewport of the page rendered at scroll 0, not the full document.
- `cropToViewport` assumes a full-document capture. It crops from `scrollY * ratio`, which only keeps the part of the first viewport below `scrollY`. When `scrollY * ratio` is past the bitmap height it returns the uncropped blob, so the user gets the top of the page.
- Fixed elements (the sidebar) are cropped with the rest, so only their lower part shows.
- Scroll position is not serialized by html-to-image (SVG `foreignObject`), so any scrolled inner container (sidebar menu, a table with `overflow-auto`, a scrolled dialog body) also renders at scroll 0.

The annotation canvas works on whatever image it gets, so annotations land on the wrong content.

## Proposal
1. Capture exactly the viewport. Call `toBlob` with `width: window.innerWidth`, `height: window.innerHeight` and shift the in-flow content by the window scroll on the cloned root (for example `style: { marginTop: -scrollY px, marginLeft: -scrollX px }`). Fixed elements must stay where they are on screen. Delete `cropToViewport` once the capture is viewport-sized.
2. Sticky elements: an element that is currently stuck must appear where the user sees it. Handle it in the same live-DOM preparation step as `swapCanvasesForImages` (apply an offset, restore in the `finally`).
3. Scrolled inner containers: for every element with a non-zero `scrollTop`/`scrollLeft` that is visible in the viewport, render its content at its current scroll offset (translate its children for the capture, restore after). Do it without a visible jump on the live page; if the only way is to touch the live DOM, keep the change inside the same `finally` restore as the canvas swap.
4. Keep `pixelRatio: min(devicePixelRatio, 2)`. The image must be `innerWidth * pixelRatio` by `innerHeight * pixelRatio`.
5. Add `scroll: "<scrollX>,<scrollY>"` to `buildContext()` so the GitHub issue says where the user was on the page.

## Tests
`web/dash0/e2e/bug-report.spec.ts`, using a fixture injected with `page.evaluate` (no dependency on seed data size), then `window.dispatchEvent(new Event("feedback:open"))` and reading the preview `<img>` in the dialog by drawing it to a canvas:

- Fixture: a 4000px tall block with a red band at document y 0-200 and a green band at document y 2000-2200, plus a `position: fixed` blue square at the bottom-right corner.
- Scrolled: `scrollTo(0, 2000)`, open the report. Image size equals viewport x pixel ratio. The top rows are green, no red pixel anywhere. The blue square is at the bottom-right.
- Positive control: same fixture at `scrollY` 0. Top rows are red, the blue square is at the bottom-right. This proves the green check is not passing by accident.
- Bottom of the page (`scrollTo(0, 1e6)`): image is full viewport height (regression for the "returns the uncropped blob" path).
- Inner scroll container: a 300px `overflow: auto` box with a 2000px child, a yellow band at child y 1000, scrolled to 1000. The box area in the image is yellow. Control: same box at scroll 0 is not yellow.
- Mobile viewport (375x667, already used in the file): scrolled capture is 375 x 667 times the pixel ratio and shows the scrolled band.
- Run the existing annotation tests in the same file: unchanged behaviour.

## To verify
- Whether `position: fixed` inside html-to-image's `foreignObject` is relative to the capture box, so a `marginTop` offset on the root leaves fixed elements in place. If not, pick another offset strategy (wrap in-flow content, or offset fixed elements back) and say which in the commit.
- Whether `modern-screenshot` (a maintained fork of html-to-image) handles window and element scroll offsets natively. Only switch library if the html-to-image approach cannot pass the tests above.
- That the charts canvas-to-img swap from spec 2026-10-09-01 still renders at the right place on a scrolled page (a chart below the fold, scrolled into view).

## Open questions
- Offer a "full page" capture toggle in the dialog? Recommended: no. Spec 2026-10-09-01 settled on viewport only, and the bug is about making that work.

## Outcome
- Implemented in `useFeedback.ts`: viewport-sized capture with the window scroll as a negative margin on the cloned root, `flattenInnerScroll` for scrolled inner containers, `cropToViewport` removed, `scroll` added to the report context.
- `position: fixed` inside the `foreignObject` stays relative to the capture box, so the margin offset leaves fixed elements in place. Sticky elements also render where the user sees them without extra handling, so item 2 needed no code.
- No library switch.
- The 4 new E2E tests fail on the previous code (scrolled: red band at top; bottom: top of page; inner box: white; mobile: wrong band) and pass with the fix. The 11 existing bug report tests still pass.
