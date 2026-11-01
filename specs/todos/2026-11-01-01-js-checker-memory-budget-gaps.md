---
model: opus
effort: high
pr: batch
automerge: false
---

# The JS check memory budget still lets one call allocate GBs: rope flattening, `concat`, `split`, `replace`, and typed arrays built from an array-like

## Problem

Found by the security scan of 2026-11-01 (finding AB1).

**Severity: medium.** Anyone who can save or run a `js` check can trigger it. On SaaS that is anyone (open sign-up, the org creator is its admin). This is the same impact as AA1 (`specs/done/2026/10/2026-10-31-01-js-checker-memory-budget.md`): every tenant's checks on the shared worker stop until it restarts, and the next scheduled run kills it again.

The AA1 fix (#728) charges a fixed list of builtins against a 64 MiB per-run budget (`server/internal/checkers/checkjs/memory.go:166-205`). goja checks `vm.Interrupt` only between instructions, so any other native path that allocates in proportion to its input still runs to completion before the timeout can act. #728 already listed string `+` doubling, `replaceAll`, `JSON.stringify` and `TextEncoder` as known gaps and promised a separate spec for process isolation. That spec was never filed. Four paths are open today. The first two turn the "known gap" into a one-line crash, not a slow loop:

1. **Rope flattening.** goja builds `a + b` as a `concatString` rope when the result is over 32 chars (goja `string_concat.go:13`, `:24-38`). Doubling a string 30+ times is about 30 instructions and allocates almost nothing. The first operation that needs the flat string (`charAt`, a comparison, `Number()`, passing it to a binding) calls `flatten()`. That call does `StringBuilder.Grow(length)` for the full length and copies it in a Go loop, in one native call.
   - Verified on 2026-11-01 against the goja version pinned in `server/go.mod`, with an interrupt set at 50 ms: a 2^28-character rope flattened in one call allocated 256 MB. The interrupt fired only after it.
   - A few more doublings ask for tens of GB.
2. **Dense-array `concat` / `slice` doubling.** `memory.go:409-411` leaves `concat`, `slice`, `splice`, `flat` and `filter` uncharged because "holes stay holes". On a dense array each call copies every element into a new slice in one native call, so repeated `a = a.concat(a)` doubles the live memory per call, far faster than the timeout.
3. **`String.prototype.split`, `replace`, `replaceAll`** are not wrapped:
   - `split("")` on a string at the budget's edge returns one array slot (16 bytes, plus a string value) per character: a 16× or larger multiplier, uncharged.
   - `replace`/`replaceAll` with a global pattern and a replacement that expands each match (`$&` repeated) produce an output equal to the input times the expansion, in one native call.
4. **Typed array from an array-like with a `length` getter** (`memory.go:687`). `chargeTypedArrayCtor` charges `r.lengthOf(obj)` and then passes the original object to goja. goja reads `length` again (`builtin_typedarrays.go:1460` `typedArrayFrom`) and allocates that many elements. A getter that answers 0 on the first read and a huge value on the second gets a multi-GB buffer for free.
   - `capArrayFrom` and the copy family already defend against this with `snapshotLength` (`memory.go:505-529`). This constructor path does not.
   - This one is a bug in the AA1 fix itself, not a missing builtin.

Out of scope here: the regexp backtracking CPU wedge (separate spec `2026-11-01-02`), and private agents, which run their own org's scripts.

## Proposal

1. Close the four paths in `server/internal/checkers/checkjs/memory.go`:
   - **Typed-array source:** in `chargeTypedArrayCtor`, pass goja a source whose `length` is pinned, either through `snapshotLength` (non-array objects behind the length-pinning Proxy) or by converting it to a real array of the charged length. The charge must be computed from the same value goja reads.
   - **`concat`, `slice`, `splice`, `flat`, `filter`:** charge the result's size, `jsValueSlotBytes` per element. For `concat`, that is the receiver length plus each spreadable argument's length (lengths read through `snapshotLength`). Replace the "holes stay holes" comment with the dense-array reasoning.
   - **`split`:** charge `(number of parts) × jsValueSlotBytes`, bounded above by `len(input) + 1` (or by `limit` when given), before the call.
   - **`replace` / `replaceAll`:** charge an upper bound of the output, `len(input) + matches × len(expanded replacement)`. A cheap bound is fine: `len(input) × (1 + len(replacement))` when the pattern can match the empty string or each character.
   - **String concatenation:** goja exposes no hook on `+`, and the flattening happens inside goja. No builtin cap can close it: see step 2 and the Open questions.
2. File and start the long-term fix #728 promised: run `js` checks in a child process (or a dedicated worker pool) with a hard memory limit (rlimit / cgroup) and a kill on timeout. The builtin caps are a denylist and cannot cover the `+` rope.
3. Update `web/docs/docs/features/javascript-checks.md` (the budget section near line 660) to list the newly charged builtins.

## Tests

In `server/internal/checkers/checkjs/memory_test.go`. Each regression test must fail before the fix, and must stay harmless if the cap is missing: use sizes of a few hundred MB at most, a short interrupt, and assert the run's duration and the `memory budget exceeded` message.

- A typed array built from an array-like whose `length` getter answers 0 then a large value is refused, and nothing large is allocated.
- Repeated `concat` of a dense array, and `slice` of a dense array over the budget, are refused.
- `split("")` and a `replaceAll` with an expanding replacement on an input near the budget are refused.
- Rope: a script doubling a string past the budget and then reading one character ends with an ERROR result quickly. Depending on the option chosen, the refusal comes from the budget or the process limit.
- Positive control: a script using `concat`, `slice`, `split`, `replace`, `replaceAll`, string `+` and `new Uint8Array([1,2,3])` on small inputs returns UP with the expected values.
- `docs_examples_test.go` still passes.

## To verify

- Whether a hook exists in the pinned goja to cap a string's length at concatenation or flattening (none was found: `toStringLen` and `unicodeStringBuilder.Grow` only refuse negatives).
- `JSON.stringify` with an indent on a large nested structure, `TextEncoder.encode`, `String.fromCharCode.apply` and `Array.prototype.flat` on nested dense arrays: amplification factor of each.
- How the SaaS fleet runs check workers (same pod as the API or not), to size the process-isolation work.

## Open questions

- How to close the `+` rope? **Recommended:** process isolation (step 2) as the real fix, delivered in this spec's PR or the next one. Meanwhile, a cheap stopgap: wrap every binding that receives a string from the script (`http.*`, `console.*`, `solidping.*`, the result) so it refuses one longer than the budget. This only covers the flatten paths through bindings, so say so in the docs.
- Ship the builtin caps (step 1) before process isolation? **Recommended:** yes. They are small, testable, and close the one-line cases that need no `+`.
