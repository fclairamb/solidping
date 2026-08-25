---
model: opus
effort: high
---

# `useChartWindowResults` "fetches raw over the whole window when pass 1 returns nothing" flakes under load — in two-pass fetch code that shipped in v0.18.0

## Problem

The dash0 unit test at
[use-chart-window.test.tsx:210](web/dash0/src/api/use-chart-window.test.tsx:210) —
`useChartWindowResults` › *"fetches raw over the whole window when pass 1 returns
nothing"* — fails intermittently.

Observed on 2026-08-25:

- `cd web/dash0 && bun run test:unit` failed once with **836/837**, this test
  failing at **1019 ms** with a `@testing-library` `waitFor` timeout
  (`checkCallback` in `wait-for.js`). An immediate re-run passed **837/837**.
- Run in isolation, `bunx vitest run src/api/use-chart-window.test.tsx` passes
  **3/3**.
- 1019 ms against `waitFor`'s **1000 ms** default is the surface tell: under
  full-suite parallel load the assertion overshoots the default window.

### This is NOT pre-existing — it is new code that shipped in v0.18.0

This was initially triaged as pre-existing and unrelated to feature work. **That
was wrong, and it inverts the priority.** Both the test and the two-pass fetch it
exercises were introduced in the v0.18.0 batch, on 2026-08-23:

- `43a551c88` *test: pin the seam window, the progressive render and the polling
  scope* — **created** this test file's body (+224 lines).
- `99237dbeb` *fix: anchor the seam on the bucket edge and resolve the chart
  window once* — changed the hook itself
  ([hooks.ts](web/dash0/src/api/hooks.ts), [chart-window.ts](web/dash0/src/lib/chart-window.ts))
  and extended this test (+157 lines).

So: brand-new two-pass raw-fetch logic, brand-new test, flaking within two days,
and **already released to production in v0.18.0**. The "just a slow machine"
reading is the convenient one, not the established one. Treat a genuine race in
the shipped hook as the leading hypothesis until it is actually ruled out.

### Correcting two details from the original report

1. **The hook file path in the report does not exist.** There is no
   `web/dash0/src/api/use-chart-window.ts`. The hook is `useChartWindowResults`
   at [hooks.ts:1515](web/dash0/src/api/hooks.ts:1515), and the window/seam logic
   it depends on is in [chart-window.ts](web/dash0/src/lib/chart-window.ts).
   There is a relevant comment about the raw-fetch ordering at
   [hooks.ts:1436](web/dash0/src/api/hooks.ts:1436).
2. **The failing assertions are at :216 and :217 only.** The report also named
   lines 263–264, but those belong to a *different* test — *"serves the chart and
   the check-detail route from one request per tier"*
   ([:248](web/dash0/src/api/use-chart-window.test.tsx:248)). Do not modify that
   test on the strength of the original report.

### Why the test is wall-clock exposed

This test runs on **real** timers — the only `vi.useFakeTimers` in the file is at
[:286](web/dash0/src/api/use-chart-window.test.tsx:286), scoped `toFake: ["Date"]`
inside a different test, and there is a real 50 ms sleep at
[:313](web/dash0/src/api/use-chart-window.test.tsx:313). There is also precedent
for papering over this: the poll-tick test already carries an explicit
`{ timeout: 3000 }` at
[:332](web/dash0/src/api/use-chart-window.test.tsx:332-334), so someone already
hit this class of problem and bumped exactly one call.

Why it matters beyond CI noise: the test's own comment calls it *"the control
that raw IS fetched and its rows do reach the series."* If that control is
unreliable, a real regression in the raw-fetch path gets waved through as "the
flaky one" — and the code it guards is live in production.

## Proposal

1. **Rule out a real defect in the shipped hook BEFORE touching the test.**
   Investigate whether `useChartWindowResults`
   ([hooks.ts:1515](web/dash0/src/api/hooks.ts:1515)) has a genuine race in its
   two-pass raw fetch (pass 1 returns nothing → pass 2 refetches raw over the
   whole window) that only surfaces when the event loop is contended — a real
   user on a loaded browser hits the same contention. Pay particular attention to
   what `99237dbeb` changed: anchoring the seam on the bucket edge and resolving
   the window once. If the hook is at fault, **fix the hook**; a longer timeout
   would be hiding a production bug.

2. **Reproduce deliberately, don't wait for luck.** Run the full suite in a loop,
   and/or run this file with the machine loaded, until you have a failure you can
   inspect. A hypothesis that was never reproduced is not a root cause.

3. **Prefer determinism over a bigger timeout.** If it genuinely is only a timing
   artifact, remove the wall-clock dependency — drive the two passes with fake
   timers and/or await the mocked fetches directly, so the assertion cannot
   depend on machine load. Fall back to an explicit `{ timeout: 3000 }` on :216
   and :217 (matching the :332 precedent) only if determinism proves impractical,
   and say why.

4. **Don't leave the same landmine next door.** The sibling default-timeout
   `waitFor` calls (:164, :165, :200, :229, :241, :263, :264, :297, :307) share
   the identical exposure. Either apply the same treatment or state explicitly
   why they are not at risk. De-flaking only the test that happened to lose the
   race this week is not a fix.

5. **Prove it.** A single green run proves nothing — the bug *is* intermittency.
   Demonstrate the test passing repeatedly under the condition that reproduced
   the failure, and report exactly what was run and how many iterations.

6. If the outcome is "hook is correct, test was wall-clock fragile", land as
   `test(dash0)`. If a real race is found, land as `fix(dash0)` and note in the
   commit body that the defect shipped in v0.18.0.

## Notes

- Gate: `cd web/dash0 && bun run test:unit`. Baseline is **837/837**; anything
  below that is a regression.
- Do not relax the eslint config or the vitest config to make this pass.

## Implementation Plan

1. **Rule the hook out first (proposal §1).** Read `useChartWindowResults`
   ([hooks.ts:1515](web/dash0/src/api/hooks.ts:1515)), `useResultTiers`
   ([hooks.ts:1426](web/dash0/src/api/hooks.ts:1426)) and everything `99237dbeb`
   touched in [chart-window.ts](web/dash0/src/lib/chart-window.ts), and decide
   whether a contended event loop can make the two-pass fetch produce a *wrong*
   result rather than merely a late one. Only if it cannot does the test become
   the suspect.

2. **Reproduce deliberately (proposal §2).** Loop `bun run test:unit`, first
   idle and then with the machine saturated, and — because the flake rate is far
   below one in a hundred runs — also build a *positive control* that forces the
   suspected condition and shows it produces the exact reported symptom
   (`checkCallback` in `wait-for.js`, ~1000 ms, that one test only). A hypothesis
   that only ever failed by luck is not established.

3. **Fix the cause, deterministically (proposal §3).** Remove the wall-clock
   dependency from the fixtures and the assertions rather than widening the
   timeout: freeze `Date` for the whole `describe` with
   `vi.useFakeTimers({ toFake: ["Date"] })` (timers stay real, so react-query's
   poll interval and `waitFor` are unaffected), and sample `Date.now()` **once**
   per fixture builder so rows built in one call cannot land on two different
   milliseconds.

4. **Treat every sibling `waitFor`, not just the one that lost the race
   (proposal §4).** Freezing the clock covers :164, :165, :200, :216, :217, :229,
   :241, :263, :264, :297 and :307 in one move for the *unsatisfiable-predicate*
   class. For the residual *slow-machine* class, apply the file's existing
   `{ timeout: 3000 }` precedent uniformly via `configure({ asyncUtilTimeout })`
   instead of decorating one call site, and drop the now-redundant per-call
   option at :332.

5. **Prove it (proposal §5).** Re-run the positive control (must now pass),
   then the file and the full suite repeatedly, idle and under load, and report
   the counts.

6. Land as `test(dash0)` if the hook is exonerated, `fix(dash0)` if not.
