---
model: opus
effort: high
---

# The steady-state checks-list refetch guard is flaky in CI

## Problem

`web/dash0/e2e/live-updates.spec.ts:487` — *"steady-state results do NOT
refetch the checks list (only transitions do)"* — failed on its first attempt
and passed on retry during the CI run for PR #212
(run [31571633122](https://github.com/fclairamb/solidping/actions/runs/31571633122)),
reported by Playwright as `1 flaky`.

The assertion that failed:

```
Error: a steady-state result write must not refetch the checks list
expect(received).toBeLessThanOrEqual(expected)
Expected: <= 1
Received:    3
    at web/dash0/e2e/live-updates.spec.ts:542:9
```

This test guards the v0.12.0 performance fix — the one where the checks list
was burning a CPU core because every live result event refetched the whole
list. A flaky guard on a performance regression is worse than no guard: it
trains everyone to re-run the job, which is exactly how the regression it
protects against would slip back in unnoticed.

Counting 3 list fetches where at most 1 is allowed is not a timing wobble
around a threshold — it is either a real refetch storm that only reproduces
under CI's slower timing, or the test attributing fetches to the wrong window
(e.g. counting the initial mount's fetches, or fetches triggered by a
*previous* step of the same test that had not settled before the counter
started).

## Proposal

Root-cause it rather than raising the threshold or marking it `flaky`.

1. Determine which of the two it is. Instrument the counted requests with
   their timing relative to the steady-state write, and reproduce under CI-like
   conditions — a cold start with a single worker, which is where it failed.
   The wall-clock-landmine possibility is worth ruling out explicitly: check
   whether the extra fetches correlate with a polling boundary (the list polls
   at 30s) landing inside the measurement window.
2. If it is a real refetch storm, fix the invalidation so a steady-state result
   event does not invalidate the checks-list query — and note that this branch's
   org-deletion work touched dash0 query invalidation, so that is the first
   place to look for a new trigger.
3. If it is the measurement window, make the counter start from a deliberate
   barrier (wait for the list query to be idle before arming it) instead of
   assuming the page has settled.
4. Either way the test must end up asserting the same property it asserts today
   — at most one list fetch for a steady-state write — with a positive control
   proving the counter still catches a genuine transition-driven refetch.

## Acceptance criteria

- The root cause is identified in writing (storm vs. measurement window), with
  evidence, not a guess.
- The test passes on first attempt across repeated CI-equivalent runs.
- The positive control still fails if the refetch-on-steady-state behavior is
  reintroduced.
