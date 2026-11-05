---
model: opus
effort: medium
pr: batch
automerge: false
---

# A JS check's child process can make the parent allocate GBs through the values it sends back

## Problem

Found by the security scan of 2026-11-03 (finding AD1). Severity: medium. Anyone who can save or run a `js` check can trigger it (on SaaS, anyone).

The process isolation of #742 (spec `2026-11-02-06`) moves the script into a memory-limited child, so a runaway allocation kills the child, not the shared worker. But the parent still decodes, without a budget, two things the child sends:

- **Binding call arguments.** For every call to a served binding (`http`, `solidping`, `browser`, `tcp`, ...: `hostGlobals`, `checker.go:517`), the child sends the arguments as JSON text (`isolation_child.go:227-258`, up to `maxChildFrameBytes` = 32 MiB, `isolation_wire.go:24`). The parent rebuilds them with its host VM's `JSON.parse` (`isolation.go:410-443`, `:418`).
  - The flatten guard on the host's `JSON.parse` (`memory_builtins.go:89`) only charges the length of the input text. It does not charge the objects the parse creates.
  - Compact JSON amplifies a lot in goja: an empty array costs 3 bytes of text and hundreds of bytes as a goja object.
  - The child only needs to produce the text. It can do that cheaply, because `encodeArgs` reads `JSON.stringify` from the script's global scope at call time (`isolation_child.go:228`), and a script can replace it.
  - **Measured** (scratch test, macOS): a script holding a 24 MB string made one binding call. The **parent** heap peaked at **4.8 GB**. The parse is one native call, so the run's timeout cannot stop it, and the script can repeat it until the timeout.
- **The result frame.** The `Done` frame (`isolation.go:365-367`) is decoded with `encoding/json` into `map[string]any`, also up to 32 MiB. Measured: a 3 MB output became about 100 MB of parent heap, so a full frame reaches about 1 GB.

Impact: one tenant's check can OOM-kill the shared worker (or the API node running checks), the AA1/AC2 class that isolation was meant to end.

## Proposal

1. Bound what the parent builds from a call, not only the frame size:
   - Read `JSON.stringify` for `encodeArgs` from the intrinsics captured before the script runs, not from the script's global scope (`isolation_child.go:228`). The script can still build large arguments, but then they are charged to the child's budget.
   - In the parent, refuse arguments before parsing when they hold too many JSON values: count the structural tokens (`[`, `{`, `,`, `:`) in one linear scan, with the same limit as `encodeMaxNodes` (`isolation.go:56`). Also lower the call-frame cap to what a binding actually needs (for example 4 MiB; `http` request bodies are the largest legitimate argument).
   - Or charge the parse to a parent-side budget for the run (64 MiB, like `maxScriptAllocBytes`), counting the values it creates.
2. Do the same for the `Done` frame: apply a node or byte budget while decoding (`json.Decoder` token walk), and give the result frame its own, smaller cap (the output is stored with the result anyway).
3. Refusals give an `error` result naming the limit, like the child's memory-limit message.
4. Update `web/docs/docs/features/javascript-checks.md` (the isolation section) to say what the parent bounds.

## Tests

- `server/internal/checkers/checkjs/isolation_test.go`:
  - a script that replaces `JSON.stringify` and makes one binding call with about 24 MB of empty-array text. It must end as an `error` result, and the parent's peak heap (sampled with `runtime.ReadMemStats` during the run) must stay under a bound such as 256 MB. This fails before the fix (about 4.8 GB).
  - the same with honest arguments: `JSON.stringify` untouched, an array of 8M references to the same empty array.
  - a script returning an output with a few million empty arrays: an `error` result naming the limit, and the parent stays under the bound.
  - positive controls: an `http.post` with a 1 MB body and a result with a normal output (a few hundred keys) still work under isolation.

## To verify

- The largest argument a legitimate binding takes (`http` request bodies, `browser` scripts), to pick the call-frame cap.
- Whether `normalizeNumbers` (`isolation.go:366`) copies the decoded output, which doubles the cost.
