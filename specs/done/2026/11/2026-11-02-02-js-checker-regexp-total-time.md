---
model: opus
effort: medium
pr: batch
automerge: false
---

# JS check regexp: a global replace chains 2 s matches into hours of uninterruptible CPU

## Problem

Found by the security scan of 2026-11-02 (finding AC3). Severity: medium, the AB2 class. Anyone who can save or run a `js` check can trigger it, which on SaaS means anyone.

The AB2 fix (#735, `server/internal/checkers/checkjs/regexp.go:17`, `:34`) sets `regexp2.DefaultMatchTimeout` to 2 s. That bounds one match, not one call:

- `String.prototype.replace` / `replaceAll` / `match` / `matchAll` / `split` and `RegExp.prototype[Symbol.replace]` with a global regexp run their whole match loop natively, inside one goja call. `vm.Interrupt` is never consulted.
- Every match gets a fresh 2 s deadline.
- A pattern that backtracks for about 1.5 s and then matches (a lookahead forces regexp2), on an input repeating that segment N times, runs about N × 1.5 s in one call.
- Measured on the pinned goja: 10 segments ran 29.5 s past a 300 ms timeout and returned no error. With N = 1,000 the input is only 25 KB, and keeps a core busy for hours.

The worker watchdog frees the slot, but the goroutine keeps spinning. Each scheduled run (or `run-now`) pins one more core, so a SaaS worker saturates within minutes, until a restart.

## Proposal

1. Bound the total regexp2 time of a run, not just one match. Options, in order of preference:
   - Wrap the global-regexp entry points (`String.prototype.replace`, `replaceAll`, `match`, `matchAll`, `split`, `search`, and the `RegExp.prototype` `Symbol.replace`/`Symbol.match`/`Symbol.matchAll`/`Symbol.split` already guarded in `memory_strings.go:96-108`). For a regexp2-backed pattern, drive the loop from Go one `exec` at a time, and check the run's deadline (`ctx`) between matches, throwing the timeout. Keep the RE2 (Go `regexp`) fast path untouched: it is linear.
   - Or lower the per-match timeout and charge each match's elapsed time against a per-run regexp budget, refusing the call once the budget is spent.
2. Report it like today's timeout: `regexpTooLongMessage()` (`regexp.go:24`), status `error` or `timeout`.
3. Document the per-run bound in `web/docs/docs/features/javascript-checks.md` next to the 2 s per-match note.

## Tests

In `server/internal/checkers/checkjs/regexp_test.go`:

- A global `replace` (and `replaceAll`, `matchAll`, `split`) of a lookahead pattern that takes about 0.2 s per match, over 50 segments, with a 500 ms check timeout, ends within about 1 s as `timeout`/`error`. Size the per-match cost so the test stays short without the fix (no more than about 10 s).
- Positive control: a global `replace` with a lookahead pattern over a small input returns the expected string. A non-global regexp2 match still works.
- The existing single-match timeout test still passes.
