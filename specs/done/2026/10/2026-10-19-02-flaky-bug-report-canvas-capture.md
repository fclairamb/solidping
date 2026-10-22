---
model: sonnet
effort: medium
---

# Flaky e2e: bug-report "canvas charts appear in the capture" opens no dialog

## Problem
`web/dash0/e2e/bug-report.spec.ts:334` failed on its first attempt and passed on retry on CI
(run 39250888645, job 118056664782, "E2E Tests (1/2)", PR #611). The failure is at line 366,
`expect(page.getByTestId("feedback-comment")).toBeVisible()`: "element(s) not found", so the
dialog never opened. It passes locally (16/16 on a side-car). Flaky tests are bugs: root-cause
it, never re-run to green, never weaken the assertion (`wiki/testing/flaky-tests.md`).

Leading hypothesis (unconfirmed): the test stubs `/api/v1/config` with `bugReport.enabled`,
reloads, waits for `networkidle`, then dispatches `window` event `feedback:open` once. The
listener is only registered when `useFeedback` has `enabled === true`
(`web/dash0/src/components/feedback/useFeedback.ts`, the "Test hook" effect after line ~100;
`open()` returns early if `!enabled`). `networkidle` does not guarantee the config response
was applied and React re-rendered, so the one-shot event can fire before the listener exists
and is lost. The same pattern is used at lines 60-80, 113, 192, 363 and 420.
A second, less likely cause: `open()` awaits `swapCanvasesForImages()` and `toBlob` before
`setIsOpen(true)`, so a slow capture (test adds a 100x100 canvas and a tainted one) delays the
dialog past the default 5 s expect timeout on a loaded CI runner.

## Proposal
1. Reproduce first: on a side-car (`wiki/testing/local-e2e.md`, never the :4000 devloop), run
   `bunx playwright test e2e/bug-report.spec.ts:334 --repeat-each=50 --workers=4` (add CPU
   throttling or `--workers` above core count to mimic CI). Confirm which hypothesis fails.
2. If the listener race: make the test wait for a readiness signal before dispatching, e.g.
   the bug icon from the feedback trigger becoming visible (it renders only when enabled), or
   `expect.poll`/`toPass` that re-dispatches until the dialog opens is NOT acceptable
   (masks the race). Prefer waiting on the visible trigger, in a shared helper used by every
   `feedback:open` call site in `e2e/bug-report.spec.ts` (lines 60-80, 113, 192, 363, 420).
   If no stable trigger exists, expose a `data-testid` readiness marker in the app instead.
3. If the capture is slow: find why in `swapCanvasesForImages` / `toBlob`
   (`useFeedback.ts`), fix the cost, or open the dialog before the capture finishes only if
   that is the intended UX. Do not just raise the timeout.
4. Verify: `--repeat-each=100` green on the affected test and the whole file, then open a PR
   titled `fix(e2e): ...` (or `fix(dash0): ...` if the app changed).

## Tests
- `e2e/bug-report.spec.ts` "canvas charts appear in the capture; a tainted canvas does not
  break it": 100/100 passes with `--repeat-each` under load, with the original assertions
  unchanged.
- Negative control: the "feature off" test (line 88) still proves no dialog opens when
  `bugReport.enabled` is false, so the readiness wait must not make it pass vacuously.
- Whole `bug-report.spec.ts` 16/16, repeated.

## To verify
- The bug icon is `data-testid="feedback-button"` (`web/dash0/src/components/feedback/FeedbackButton.tsx:15`);
  check it renders only when `bugReport.enabled`, so it can serve as the readiness signal.
- Whether the race hypothesis reproduces, before changing anything.
