---
model: opus
effort: high
pr: batch
automerge: false
---

# A JS check can exhaust the worker's memory: the timeout does not stop a native allocation

## Problem

Found by the security scan of 2026-10-31 (finding AA1).

**Severity: medium.** Anyone who can save or run a `js` check can trigger it. On SaaS that means anyone, since sign-up is open and the creator of an org is its admin. It is the same class as J1, K1 and D5 (cheap, repeatable memory exhaustion of a process shared by every tenant).

The `js` check type runs a tenant script in an in-process goja VM (`server/internal/checkers/checkjs/checker.go:139` `Execute`, `:439` `newJSRuntime`). Its only resource guards are:

- a time budget: the context deadline (at most `MaxTimeout` = 30 s, `checkjs/config/config.go:46`, `:366`), turned into `vm.Interrupt("timeout")` (`checker.go:194-198`);
- a payload budget (`checker.go:413-423`) that caps bytes received from the network, console and sockets, not what the script allocates.

The original design (`specs/done/2026/03/2026-03-22-javascript-checker.md:386`, `:575`) accepted "no memory limit (goja limitation), the timeout is the safety valve". That valve does not hold:

- `vm.Interrupt` is only checked between VM instructions. A single call to a native builtin runs to completion first. For example `String.prototype.repeat` (goja `builtin_string.go:562-585`) grows a `strings.Builder` to the full result size and fills it in a Go loop.
- Verified on 2026-10-31 with the goja version pinned in `server/go.mod`: with an interrupt set at 50 ms, a 300 MB `repeat` still ran to completion (1.18 s). Only then did the interrupt fire.
- A one-line script asking for a result of several GB therefore allocates and touches all of it before any guard runs. The process is OOM-killed (the `GOMEMLIMIT` soft cap applied in `main.go:232` only makes the GC work harder, it refuses no allocation).

Other native paths in the same class: `padStart`/`padEnd`, `Array.prototype.join`, `ArrayBuffer`/typed arrays with `fill`. A pure-JS allocation loop is interrupted, but can still allocate several GB within the 30 s budget.

Reach:

- `js` is enabled by default on every deployment, SaaS included. The SaaS rule in `checkerdef/activation.go:33-41` only turns off unsafe **plugin** types; `js` is built in (`checkerdef/types.go:491`).
- Scheduled runs (minimum period 30 s) and `POST .../checks/:checkUid/run-now` (`app/server.go:1541`) run the script on the shared check workers. Killing a worker interrupts every tenant's checks on it until it restarts, and the next scheduled run kills it again.
- The AI check runner executes JS in the API process (`aichecks/runner.go:85-102`), but there the script comes from the model.
- Private agents run their own org's scripts: self-inflicted, out of scope.

## Proposal

1. Cap the native builtins that build a large value in one call, before they allocate. Install the caps in `newJSRuntime` / `registerGlobals` (`checker.go:439`, `:448`), on the VM's own prototypes:
   - `String.prototype.repeat`, `padStart`, `padEnd`: refuse (throw a `RangeError`) when the result would exceed a per-run byte budget;
   - `Array.prototype.join`, `new Array(n)` / `Array(n)` and `length` writes on arrays: same rule;
   - `ArrayBuffer`, `SharedArrayBuffer` and the typed-array constructors: same rule;
   - draw every check from one per-run allocation budget (for example 64 MB, a named constant next to the payload budget), so many medium allocations add up.
2. Make the refusal a normal script error: the check reports ERROR with a message saying the memory budget was exceeded, and the worker keeps running.
3. Long term (see Open questions): run `js` checks in a child process or a separate worker pool with a hard memory limit (rlimit or cgroup), as the original spec suggested. Builtin caps are a denylist, and goja may add native paths later.

## Tests

- `checkjs` regression test (fails before the fix): a script calling `"x".repeat(n)` with n far above the budget returns an ERROR result quickly (well under the timeout) with the budget message. The test process must not allocate the string: assert the run's duration, and use a size that stays harmless even if the cap is missing (for example 512 MB, under a 50 ms interrupt as in the verification above).
- Same for `padStart`, `Array(n).join(...)` and `new ArrayBuffer(n)` followed by `fill`.
- Positive control: a script using `repeat`, `join`, `padStart` and a small typed array within the budget still returns UP with the expected values.
- A budget test: many allocations each under the cap but summing above the budget are refused.
- The existing `docs_examples_test.go` scripts all still pass, so the budget does not break documented examples.

## To verify

- The full list of goja builtins that allocate in proportion to an argument without an interrupt check (look for `Grow`, `make(` sized by an argument in the goja module).
- Whether `Array.prototype.fill`, `concat`, `String.prototype.replaceAll` with a long replacement, `JSON.stringify` of a large structure or `TextEncoder` allow the same single-call amplification.
- How the SaaS fleet runs check workers (same pod as the API or separate pods), to word the docs and changelog.

## Open questions

- Builtin caps now, process isolation later, or process isolation now? **Recommended:** builtin caps in this change (small, testable, closes the one-line case), and a separate spec for a memory-limited JS worker pool.
- Per-run budget value? **Recommended:** 64 MB, the same order as the payload budget, with no config knob until someone asks.
