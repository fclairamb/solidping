---
model: opus
effort: high
pr: batch
automerge: false
---

# JS check memory budget: builtins and rope paths still allocate GBs in one call

## Problem

Found by the security scan of 2026-11-02 (finding AC2). Severity: medium, the AA1/AB1 class. Anyone who can save or run a `js` check can trigger it, which on SaaS means anyone. The script runs on the check worker that every org shares (and in the API process, for the AI runner, `aichecks/runner.go:102`).

#735 extended the 64 MiB per-run budget (`server/internal/checkers/checkjs/memory.go:181-224`, `memory_strings.go`, `memory_bindings.go`). It is still a denylist. goja checks `vm.Interrupt` only between instructions, so each path below runs to completion in one native call, whatever the timeout. Each was measured on the pinned goja with a 300 ms timeout, using inputs that the budget itself accepts:

1. **Uncharged builtins that amplify their input:**
   - `JSON.stringify`: an array of 262,144 references to one 1 KiB string allocated about 1.3 GB and ran 1.9 s. With an indent on a nested array that shares its children, it reached 3.2 GB.
   - `Array.prototype.toLocaleString`: native, not routed through the wrapped `join`. The same array allocated 1.7 GB.
   - `String.prototype.normalize("NFKD")` on a string at the budget's edge, made of a character with a long decomposition: about 12 GB, 14 s. The method is flatten-guarded only, and its output is not charged.
   - `String.raw` with an array-like `raw` and a large `length`: about 5 GB, 5.6 s. Each missing element becomes `"undefined"`.
   - `encodeURIComponent`, `encodeURI`, `escape` on a CJK string at the edge: 0.6 to 1 GB.
2. **Rope flattening outside the guarded calls:**
   - `JSON.parse(rope)` flattened a 256 Mi-character rope: 2.8 GB.
   - A thrown rope: the guard runs only on a normal return (`checker.go:215-217`). Then `err.Error()` flattens the rope, and `"script error: " + …` copies it (`checker.go:341`). A 256 Mi-character error string (2 GB allocated) is put into the result's `output.error`, which is stored with the result and served by the API.
   - A rope nested more than `ropeScanMaxDepth` (64, `memory_bindings.go:14`, `:119`) levels deep in the returned value, or in a binding argument, is not counted. `Export()` then flattens it (256 MB measured, status `up`). The 1 Mi-node cap (`:12`) can be dodged the same way.
   - Documented as accepted: `s[0]`, `s == t`, `Number(s)`, template literals (docs `web/docs/docs/features/javascript-checks.md:686-693`). The docs say "only the timeout stops it". That is wrong: the timeout cannot stop a native flatten.
3. **No process isolation.** Spec `2026-11-01-01` and #728 both name it as the real fix (step 2 there). No spec was filed for it.

Impact: the same as AA1. One scheduled check OOM-kills the shared worker, and kills it again after every restart.

## Proposal

1. Close the measured paths in `checkjs`:
   - Wrap `JSON.stringify`. Pre-walk the value (cycle-safe, with the replacer and indent known) and charge an upper bound of the output before calling the original. Or rewrite it in Go with a charged builder and a deadline check, like `boundedJoin` (`memory.go:338`).
   - Route `Array.prototype.toLocaleString` through `boundedJoin` semantics, charging each element's string.
   - Charge `normalize` at the maximum expansion factor per form (NFKD/NFKC: 18×, NFD/NFC: 3× per UTF-16 unit). Charge `encodeURIComponent`, `encodeURI`, `escape` at 9× / 6× the input length. Charge `String.raw` at `length × (9 + max substitution length)`.
   - Put the flatten guard (`flattenGuard`, `memory_strings.go:138`) on `JSON.parse`, the `RegExp` constructor, the global `parse*`/`decode*`/`encode*`/`escape`/`unescape` functions, and `String()`/`Number()` called as functions.
   - Run `guardResult`'s rope scan on a thrown value too, and cap the error text placed in `output` (16 KiB, like `maxConsoleOutput`).
   - Make the rope scan refuse instead of giving up: reaching the depth or node cap counts as "over budget", not as "0 bytes".
   - Correct the docs paragraph (lines 686-693): the uncharged flatten paths are only stopped by process isolation, not by the timeout.
2. Process isolation, the real fix for the class: run `js` checks (and the AI runner's script runs) in a child process or a dedicated worker pool, with a hard memory limit (`RLIMIT_AS`/cgroup) and a kill on timeout. The parent reports a killed child as an `error` result. This step can be a follow-up PR, but it must be filed as its own spec if it is not done here.

## Tests

In `server/internal/checkers/checkjs/memory_test.go`. Each regression test must fail before the fix and stay harmless without it: use a few hundred MB at most, a short timeout, and assert the run's duration and the `memory budget exceeded` message.

- Each builtin in step 1 is refused when called on an input the budget accepts but whose output exceeds the budget: `JSON.stringify` (flat and indented), `toLocaleString`, `normalize("NFKD")`, `String.raw`, `encodeURIComponent`, `escape`.
- `JSON.parse` on a rope over the budget is refused.
- A thrown rope over the budget ends as an `error` result quickly, with an `output.error` of at most 16 KiB.
- A rope nested 70 levels deep in the returned `output` is refused.
- Positive control: small inputs to every wrapped builtin return the expected values (`JSON.stringify` with a replacer, an indent and `toJSON`; `normalize`; `String.raw` with a template; `encodeURIComponent` of a multi-byte string; a thrown `Error("short")` message kept).
- `docs_examples_test.go` still passes.

## To verify

- The maximum expansion factor of each Unicode normalization form, for the goja/`golang.org/x/text` version in use.
- Whether goja's `JSON.stringify` honours a wrapped `toJSON` / replacer, so a pre-walk computes the same structure the original serializes.
