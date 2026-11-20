---
model: opus
effort: medium
---

# The checker watchdog reports "did not honor context" after a VM pause

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/97

## Source

Daily review of the prod pod logs (24 h ending 2026-11-20 07:15 UTC), two items:

1. On one checker node, right after a known hypervisor pause (same minute: DB
   `i/o timeout`s, `Claim timed out`, `Check lease lost`), one `http` check got
   `ERROR Checker abandoned: did not honor context`, reason `ignored_context`,
   the first time ever for an HTTP check. The reporter's hypothesis: the VM was
   frozen longer than `execTimeout + abandonGrace`, so on resume the watchdog
   timer and the checker's own context deadline expire together and the
   `select` may pick the abandon branch before the HTTP goroutine returns its
   timeout. The line carries neither the elapsed time nor the exec timeout, so
   a misfire cannot be told apart from a real hang.
2. A 13-minute burst of `slow SQL query` on the server (x3.5 the baseline,
   mixed statements, up to 3.8 s, no error), with no cause in the logs.

## Problem

- `server/internal/checkworker/worker.go:1736-1781` (`runCheckerGuarded`): the
  child goroutine sends on `outcomeCh` (cap 1); the parent `select`s between
  `outcomeCh` and `time.After(execTimeout + effectiveAbandonGrace())`. When the
  process is frozen past both deadlines, both cases are ready on resume and Go
  picks one at random; and the checker may need a few ms after its context
  deadline fires to unwind and send. Either way the watchdog abandons a checker
  that did honour its context.
- `abandonCheckerExecution` (`worker.go:1786-1830`) logs at ERROR with
  `reason=ignored_context` whenever no agent outage is active
  (`r.outage.active()`), and records the `abandoned` metrics; nothing says how
  long the run took against its budget.
- Item 2 has no code lead: a DB-wide slowdown across unrelated statements, no
  timeout. Out of scope here beyond the note below.

## Why a spec

- Correctness hinging on concurrency: the watchdog's race with the checker's
  own timeout, and how a process pause is told apart from a hung checker.

## Proposal

1. **Last-chance receive.** When the abandon timer fires, do a short bounded
   receive on `outcomeCh` (e.g. up to 250 ms, a package constant, overridable in
   tests like `abandonGraceOverride`) before declaring abandonment. A checker
   that returns in that window is a normal (timeout) result, not an abandon.
2. **Detect a pause.** Measure the wall time actually spent against the expected
   budget: if `time.Since(startTime)` exceeds `execTimeout + grace` by much more
   than the grace (e.g. > 2x the budget), or a pause detector already exists in
   the worker (lease loss / claim timeout tracking), classify the abandon with a
   new reason `process_paused`, logged at WARN, not ERROR. Keep
   `ignored_context` for the real hang.
3. **Log attributes.** Add `elapsed` and `exec_timeout` (durations) to both
   abandon log lines.
4. **Tests** (`worker_test.go` or the existing watchdog tests): a checker that
   returns just after the abandon timer is not abandoned; a checker that never
   returns is still abandoned with `ignored_context`; the pause classification
   with an injected clock or start time. Run with `-race`.
5. Update the metric reason enum (`prommetrics.AbandonReason*`) and any doc
   listing the reasons.

Item 2 (slow SQL burst): no change. If it recurs at the same hour, look at a
background aggregation/compaction job or Postgres maintenance (vacuum, backup)
starting around 20:00 UTC, in a new issue.

## Open questions

1. **Pause detection signal.** Recommended: the elapsed-vs-budget ratio
   (self-contained, no new state). Trade-off: a checker hung for exactly 2x its
   budget would be misfiled as a pause; acceptable since the timer fires at 1x
   plus grace, so a real hang is caught on time and lands near 1x.

## Resolved open questions

1. Detect the pause with the elapsed-vs-budget ratio (self-contained, no new state). (autopilot: recommended answer)

## Closing the issue

This spec closes fclairamb/solidping-business#97. The implementing PR body
**must** carry a `Closes fclairamb/solidping-business#97` line. When the spec is
archived to `specs/done/`, verify the issue is closed and, if the merge did not
close it, close it by hand:

    gh issue close 97 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"
