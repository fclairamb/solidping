---
model: opus
effort: high
pr: batch
automerge: false
---

# JS checks: run each script in a memory-limited child process

## Problem

Filed by spec `2026-11-02-01` (step 2 of its proposal), which closed the
measured amplifying builtins and flatten paths of the `js` checker. The
in-process memory budget (`server/internal/checkers/checkjs/memory*.go`) stays
a denylist: goja checks `vm.Interrupt` only between instructions, so any
native call that allocates in one go runs to completion whatever the timeout.
The flattens that happen outside a call (`s[0]`, `s == t`, `+s`, a template
literal on a rope built with `+`) are still not charged, and the next goja
builtin found to amplify its input will be another one.

The script runs on the check worker every organization shares, and in the API
process for the AI runner (`server/internal/aichecks/runner.go`). One script
that allocates a few GB OOM-kills the shared process, and kills it again after
every restart. Specs `2026-11-01-01` and `2026-11-02-01` and #728 name process
isolation as the real fix for the class.

## Proposal

1. Run a `js` check (and the AI runner's script runs) in a child process of
   the same binary (a hidden subcommand), or in a dedicated pool of such
   processes, with a hard memory limit: `RLIMIT_AS` / `RLIMIT_DATA` on Linux,
   a cgroup when available, and a kill on timeout.
2. Pass the config and the env on stdin, read the result on stdout. The
   bindings that need the parent (sub-checks, `solidping.*`, the browser and
   RDP/VNC pools, the egress allowlist and the repair hold) are served over
   that channel, so their rules stay enforced in one place.
3. A child that is killed, by the limit or the timeout, is reported as an
   `error` (or `timeout`) result naming the cause. The parent never crashes.
4. Keep the in-process budget as the first line: it gives a clear
   `memory budget exceeded` message for the common cases.

## Tests

- A script that flattens a 256 Mi-character rope with `s[0]` ends as an
  `error` result naming the memory limit, and the parent process is alive.
- A script that loops forever in one native call is killed at its timeout.
- The bindings work from the child: `http`, a sub-check, `console`,
  `solidping.*`, and a refused host under a repair hold.
- `docs_examples_test.go` still passes.

## To verify

- The overhead of a child process per run on the check worker, and whether a
  warm pool is needed to keep the scheduling latency.
- How `RLIMIT_AS` interacts with the Go runtime's virtual reservations: a
  limit on RSS (cgroup `memory.max`) may be the only workable one.
