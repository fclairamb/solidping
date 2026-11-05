---
model: opus
effort: medium
pr: batch
automerge: false
---

# Calling `RegExp.prototype[Symbol.replace]` directly skips the JS memory budget

## Problem

Found by the security scan of 2026-11-03 (finding AD2). Severity: low with the default settings, medium where an operator sets `SP_JS_ISOLATION=off`. Anyone who can save or run a `js` check can trigger it.

- `String.prototype.replace`/`replaceAll` charge every substitution to the budget through `capReplace` (`server/internal/checkers/checkjs/memory_strings.go:266`).
- The `RegExp.prototype` symbol methods (`Symbol.replace`, `Symbol.split`, `Symbol.match`, `Symbol.matchAll`, `Symbol.search`) only get the flatten guard (`memory_strings.go:97-113`). For a global regexp, `execDrivenGlobal` (`regexp_deadline.go:87`) runs goja's own substitution loop. With a string replacement, goja expands `$'` (the rest of the input) natively for each match, and nothing charges it.
- A script calls the symbol method directly on a string of a few hundred KB, with a `$'` replacement. **Measured** (scratch test, 2 s timeout):
  - `SP_JS_ISOLATION=off`: the worker heap peaked at **54 GB**, and the call ran **29 s** past the timeout;
  - default isolation: the child hit its 256 MiB limit and was killed (`error` result), the parent stayed at 1 MB;
  - control: the same `$'` replacement through `String.prototype.replace` is refused by the budget.
- With isolation off (a documented switch, `web/docs/docs/features/javascript-checks.md:734`), this is the AA1/AC2 class: one check can OOM-kill a shared worker. With isolation on, the check only kills its own child, but the budget, documented as the first line of defence, is bypassed.

## Proposal

1. Route the direct `Symbol.replace` call through the same charging replacer as `capReplace` (`memory_strings.go:266-337`), for a string replacement and for a function replacement alike. The cleanest way is one wrapper installed on `RegExp.prototype[Symbol.replace]`, which `String.prototype.replace` then reuses.
2. Check the other symbol methods for the same gap when called directly: `Symbol.split` (the AC3 reimplementation), `Symbol.match` and `Symbol.matchAll` with a global regexp (the array of matches). Charge each one like its `String.prototype` twin.
3. No behaviour change for scripts within the budget.

## Tests

- `server/internal/checkers/checkjs/memory_test.go`, with isolation off (`SetIsolation`):
  - `RegExp.prototype[Symbol.replace].call(/x/g, "x".repeat(200000), "$'")` must give a `RangeError: memory budget exceeded` result, quickly (under 1 s). It fails before the fix (tens of GB, past the timeout).
  - the same with `Symbol.split` and `Symbol.match` on large inputs.
  - positive control: `RegExp.prototype[Symbol.replace].call(/a/g, "banana", "$'")` still returns the expected string, and a `$&`/`$1` replacement on a small input is unchanged.
