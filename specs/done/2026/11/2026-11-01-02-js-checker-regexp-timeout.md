---
model: opus
effort: medium
pr: batch
automerge: false
---

# A JS check's regular expression can backtrack forever: the timeout cannot stop it and each run leaks a spinning goroutine

## Problem

Found by the security scan of 2026-11-01 (finding AB2).

**Severity: medium.** Anyone who can save or run a `js` check can trigger it. On SaaS that is anyone (open sign-up, the org creator is its admin). It is the CPU twin of AA1: an availability issue in a process shared by every tenant, from a free account, repeatable.

- goja runs a JS regular expression on Go's `regexp` (linear time) when the pattern is RE2-compatible. Otherwise it uses `dlclark/regexp2`, a backtracking engine (goja `regexp.go:44-58` `compileRegexp2`). A lookahead or a backreference is enough to get regexp2.
- goja never sets `MatchTimeout`. regexp2 then uses `DefaultMatchTimeout`, which is "forever" (`regexp2` `regexp.go:22-23`, `:78`). A nested quantifier on a non-matching input backtracks exponentially, inside one native call.
- The JS checker's only stop is `vm.Interrupt("timeout")` when the context ends (`server/internal/checkers/checkjs/checker.go:193-197`). goja checks it only between instructions, so it never reaches a running match.
  - Verified on 2026-11-01 against the goja version pinned in `server/go.mod`: with an interrupt set at 50 ms, a one-line `test()` of such a pattern on a 31-character input was still running after 8 s.
- The check worker's watchdog (`server/internal/checkworker/worker.go:1627-1665`) gives up after `execTimeout + abandonGrace` and frees the worker slot. The goroutine keeps spinning, though: nothing can stop it.
  - Every scheduled run (period 30 s or more) or `run-now` adds one more goroutine pinned at 100 % of a core.
  - Within minutes every core of the shared worker is busy, and every tenant's checks slow down or time out. Only a restart clears it, and the next runs start again.
  - The `check_runner_abandoned_active` metric shows it, but nothing acts on it.
- The AI check runner also executes JS in the API process (`server/internal/aichecks/runner.go:102`). There the script comes from the model, but a user can ask the model for a given pattern.

Private agents run their own org's scripts: self-inflicted, out of scope.

## Proposal

1. Give every regexp2 match a time limit. `regexp2.DefaultMatchTimeout` is a package variable that `regexp2.Compile` copies into each `Regexp` (`regexp2` `regexp.go:78`), and goja compiles script patterns at run time through it. Set it once, at process start, before any VM runs, in `checkjs` (an `init` or the runtime constructor guarded by `sync.Once`). Use a named constant, for example 2 s, well under the 30 s check budget.
2. Make sure a timed-out match surfaces as a normal script error: the check reports ERROR with a message saying the regular expression took too long, and the worker keeps running. Map goja's error (the regexp2 timeout returned by `FindRunesMatches` / `FindStringMatch`) to that message in `resultFor`.
3. Document it in `web/docs/docs/features/javascript-checks.md`, next to the memory budget: regular expressions with lookarounds or backreferences have a per-match time limit.

## Tests

In `server/internal/checkers/checkjs/`:

- Regression (fails before the fix): a script testing a pattern with a lookahead and a nested quantifier against a non-matching input of about 30 characters returns an ERROR result within a few seconds, well before the check timeout, with the "regular expression" message. Bound the test with its own `context` deadline and a goroutine check (the run returned), so a missing fix fails instead of hanging the suite.
- Positive control: a script using a lookahead and a backreference on a normal input (for example extracting a version from a header) returns UP with the expected value.
- Positive control: an RE2-compatible pattern (Go `regexp` path) behaves as before.

## To verify

- Which regexp2 version goja resolves to in `server/go.mod` (`v1.12.0` is listed as indirect, `regexp2/v2` too), and that its `Compile` still copies `DefaultMatchTimeout`.
- How goja surfaces a regexp2 error from `exec`, `test`, `match`, `replace` and `split` (thrown JS error, Go panic, or swallowed as "no match"). If it is swallowed, a timed-out match must not be reported as a plain non-match: add a flag on the run that `resultFor` checks.
- Whether other goja users in the server (the AI runner, the plugin runtime if any) need the same limit. A process-wide default covers them all.

## Open questions

- Match time limit? **Recommended:** 2 s per match, a named constant with no config knob. A legitimate monitoring regexp finishes in microseconds.

## Resolved open questions

- (autopilot: recommended answer) Match time limit is 2 s per match, a named constant with no config knob.
