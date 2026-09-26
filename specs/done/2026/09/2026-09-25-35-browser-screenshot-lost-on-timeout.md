---
model: sonnet
effort: medium
---

# A browser check that fails by timing out never keeps its screenshot

## Problem

The browser checker is built so a capture survives a probe timeout: `Execute` opens the
session on `sessionCtx` = check timeout + `screenshotTimeout` (5s), and drives the probe on
a child `probeCtx` = check timeout, so the tab is still alive to photograph after the probe
gives up (`server/internal/checkers/checkbrowser/checker.go:139-158`).

That extra 5s does not exist. The worker hands `Execute` an `execCtx` whose deadline is
`checkTimeout + 1s` (`server/internal/checkworker/worker.go:1083`). `sessionCtx` is a child
of `execCtx`, so its real deadline is the parent's, 1s after the probe's, minus whatever
the worker spent between creating `execCtx` and entering `Execute`. When the probe uses its
whole budget, the capture starts with well under a second left and fails.

Observed on solidping.k8xp.com on 2026-09-25: a browser check on `https://www.acme.com`
with `timeout: 25s`, `screenshot: true` and a `waitSelector` that never matches. Every run
reports `down` with `durationMs: 25000`, the incident has `attachments: null`, and the
paris worker logs on each run:

```
level=WARN msg="browser check: screenshot capture failed" url=https://www.acme.com error="context deadline exceeded"
```

The same page captured fine when the failure was a missing keyword (the probe ended at about
6.6s), and a local timing of `Session.Screenshot` on that page takes about 0.34s. So the
capture itself is not slow. It gets no time.

This hits the most common capture-worthy failure: a page that hangs, or a `waitSelector`
that never appears. The JS runtime's `page.screenshot()` is likely affected the same way
when a script runs to its deadline. Verify that.

## Proposal

- Let a checker declare extra wall-clock it needs beyond its probe timeout, and have the
  worker add it to the `execCtx` deadline: for example an optional
  `checkerdef` interface `ExtraBudget(config) time.Duration` that the browser checker
  implements as `screenshotTimeout` when `cfg.Screenshot` is set, else 0. The worker
  keeps `checkTimeout` as the budget handed to the checker. Only the hard context grows.
- Account for the extra time wherever the worker reasons about occupancy (the
  cost-aware clamp, burst budget `resolveBurstBudget`, lease or heartbeat windows), so a
  30s-timeout browser check with capture on is not killed or double-scheduled at 31s.
- Apply the same to the JS checker if its `page.screenshot()` path has the same shape.
- Log the remaining budget in the "screenshot capture failed" warning, so the next
  starvation is visible without a code read.

## Tests

- Checker-level: with a fake session whose probe blocks until `probeCtx` expires and a
  parent context of `timeout + 1s`, the capture fails today. With the worker's extended
  deadline it succeeds. That is the regression test, and it must fail before the fix.
- Worker-level: a browser check with `screenshot: true` gets an `execCtx` deadline of
  `checkTimeout + 1s + screenshotTimeout`. Without `screenshot`, or for an `http` check,
  the deadline is unchanged (positive control).
- Live (`slowtests` or the live browser harness): a page whose `waitSelector` never
  appears yields a `timeout`/`down` result carrying a non-empty screenshot.
