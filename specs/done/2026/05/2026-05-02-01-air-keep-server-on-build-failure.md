# Air — keep last good server running on build failure

## Context

`server/.air.toml` currently has `stop_on_error = true`. With air v1.62, the actual order of operations on every file change is:

1. Kill the running binary (`engine.go:431`, `stopBin()`, blocks up to 5s)
2. Run the build command (`cmd = "go build -o ./tmp/solidping ."`)
3. If the build succeeds, run the new binary; otherwise behaviour depends on `stop_on_error`

Because `go build -o ./tmp/solidping .` only writes the output on success, the file at `tmp/solidping` after a failed build is still the *previous good* binary. With `stop_on_error = true`, air refuses to relaunch it, so the dev server stays dead until the developer's next successful build. With `stop_on_error = false`, air relaunches the existing on-disk binary, so the API stays up running the last good code.

This spec is about that one-line change. It is **not** about zero-downtime reload — air always kills before building, and that gap (a few seconds while `go build` runs) remains. See the companion spec `2026-05-02-zero-downtime-dev-reload.md` for the full fix.

## File to change

`server/.air.toml`, line 37:

```toml
# Stop running old binary after build failure
stop_on_error = true
```

→

```toml
# On build failure, keep running the previous good binary (tmp/solidping is
# unchanged because `go build -o` does not overwrite on error). Air still
# kills the running process before invoking the build (see engine.go:431) —
# this only governs whether the old binary is relaunched after a failed build.
stop_on_error = false
```

## Out of scope

- The kill-before-build sequence in air (cannot be changed via config).
- Replacing air with a build-then-kill wrapper. That's the other spec.
- `kill_delay`, `send_interrupt` — leave at current values (`500ms` / `true`).
- Any production reload mechanism. This file only affects `make dev` / `make dev-test`.

## Verification

1. Start `make dev-test` and confirm the server responds at `http://localhost:4000/api/mgmt/health`.
2. Introduce a deliberate Go syntax error in any file under `server/` (e.g. drop a closing brace in `internal/app/server.go`).
3. Watch air's log: build fails. Within a couple of seconds, the *previous* `tmp/solidping` should be relaunched. `curl http://localhost:4000/api/mgmt/health` should succeed again.
4. Fix the syntax error. Air rebuilds, kills the old binary, runs the new one. `health` should still respond after the brief swap.
5. Revert step 2 cleanly so the working tree is unchanged.

With the previous setting (`stop_on_error = true`), step 3 would have left the server permanently down until step 4.

## Implementation Plan

1. Edit `server/.air.toml` line 37: change `stop_on_error = true` to `stop_on_error = false` and update the inline comment as above.
2. Run the verification steps.
