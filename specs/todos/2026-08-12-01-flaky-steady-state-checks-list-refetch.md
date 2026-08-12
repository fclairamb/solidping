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

## Implementation Plan

### Root cause (measured, not guessed)

It is the **measurement window / attribution**, not a refetch storm. Reproduced
locally against a side-car `SP_RUNMODE=test` server (Postgres, `CI=true`,
`--workers=1`) with the counted requests instrumented to log their query string
and their offset from the moment the counter is armed:

```
[diag] listFetches=1
  +302ms ?with=last_result&q=E2E+Steady+State+…&internal=false&limit=100&sort=group
[diag-after-transition] listFetches=3
  +302ms ?with=last_result&q=E2E+Steady+State+…&internal=false&limit=100&sort=group
  +8970ms ?limit=10
  +8970ms ?with=last_result&q=E2E+Steady+State+…&internal=false&limit=100&sort=group
```

Three independent defects add up to exactly the CI number (1 + 2 = 3):

1. **The counter is armed before the search debounce has fired.**
   `fill("Search checks...")` is followed by `expect(row).toBeVisible()`, which
   is satisfied by the *pre-filter* list already on screen. 300 ms later the
   debounced `q=` query key changes and TanStack Query fetches the filtered
   list — **inside** the measurement window. That single fetch consumes the
   whole `<= 1` budget, leaving zero headroom for anything else.

2. **The counter matches by URL pathname, so it counts a different query.**
   `/api/v1/orgs/test/checks` is also fetched by the org layout's
   `CommandMenu` (`useChecks(org, { limit: 10 })`, key `["checks", org, …]`) —
   see the `?limit=10` line above. Both that flat key and the page's
   `["checks","infinite",org,…]` key match the `checks`/`checks` live roots, so
   **one** invalidation costs **two** against the budget.

3. **The window is not immune to a genuine `checks` transition — and the test
   manufactures one itself.** A heartbeat check is scheduled like any other
   (default period 60 s) and its passive job writes a `No heartbeat received`
   → `down` result when the last signal is missing or stale. Its first run
   fires within ~2 s of check creation and races the setup heartbeat: locally,
   1 run in 8 ended up `down` before the window even opened (the test failed
   at `row.getByText("Up")`, screenshot shows `… heartbeat-heartbeat Down 0ms`).
   Under CI load that first passive run is delayed, lands **inside** the
   window, flips `up → down`, and `publishStatusHint` →
   `PublishImmediate(KindChecks)` invalidates both checks-list roots — the +2
   above.

**The polling boundary is ruled out.** `CHECKS_LIST_POLL_MS` is 10 s (not 30 s);
every counted request is timestamped and across 8 local runs no poll tick ever
landed in the ~6 s window. The extra fetches were always the +300 ms debounce
and the transition-driven pair.

### Steps

1. `web/dash0/e2e/live-updates.spec.ts` — remove the self-inflicted transition:
   let `createHeartbeatCheck` take an optional `period`, and create the
   steady-state check with a 1 h period so its passive "no heartbeat" job
   cannot re-fire during the test.
2. Add a `waitForCheckStatus(page, token, uid, "up")` barrier that polls the API
   until the check really is `up`, re-sending a heartbeat between polls — this
   absorbs the create-vs-first-passive-job race (and fixes the second,
   independent flake where the row rendered `Down`). Use it in both checks-list
   tests that assert an `Up` badge.
3. Arm the counter from a deliberate barrier: `page.waitForResponse` on the
   list request that actually carries `q=<check name>`, so the debounced
   filtered fetch is *outside* the window.
4. Count only the page's own list query — `searchParams.get("q") === check.name`
   — so the CommandMenu's `?limit=10` query can no longer inflate the count.
5. Account for foreign transitions instead of hoping none happen: tally
   `{"type":"update","entity":"checks","kinds":["checks"]}` frames on the live
   socket during the window and allow one extra fetch per damper interval that
   could have carried one —
   `1 + min(checksHints, ceil(windowMs / LIVE_INVALIDATE_MIN_INTERVAL_MS))`.
   A reintroduced results-driven invalidation produces refetches with **zero**
   `checks` hints, so the guard keeps its teeth.
6. Positive control inside the test: keep counting past the window and assert
   the final genuine `down` transition produced at least one page-list fetch.
7. Verify: ≥10 repeats, single worker, CI-equivalent; then temporarily
   reintroduce `orgRoot("checks")`/`infiniteOrgRoot("checks")` under
   `DEFAULT_QUERY_ROOTS.checks.results` and confirm the test fails.
