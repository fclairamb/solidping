# Zero-downtime backend dev reload (build before kill)

## Context

The original ask was: "make air only kill and reload the server *after* it has been built." Air cannot do this. Reading `runner/engine.go` in air v1.62 confirms that `Engine.start()` always calls `stopBin()` (kills the running process) **before** invoking the build command, regardless of config. There is no flag — `stop_on_error`, `kill_delay`, `send_interrupt`, `pre_cmd`, `post_cmd` — that changes that ordering.

Effect today: every save triggers a 1–5 second window where the dev server is dead while `go build` runs. That window costs more than it sounds — frontend dev (`web/dash0`, `web/status0`) running concurrently makes blind requests during the gap, dev tooling (Playwright, MCP, manual curl) gets connection refused, and the dev cycle described in `CLAUDE.md` ("apply code changes, wait 5s for it to build and then test") is precisely that downtime.

The companion spec `2026-05-02-air-keep-server-on-build-failure.md` shrinks the *failure* path. This spec eliminates the *success* path downtime by replacing air for the backend with a small watcher that does build-then-swap.

## Approach

Write a tiny Go program at `server/cmd/devloop/main.go` that:

1. Watches `server/` for `.go` file changes via `fsnotify`, applying air's existing exclude rules (`tmp/`, `vendor/`, `.git/`, `testdata/`, `res/`, `apps/`, `openapi/`, `*_test.go`).
2. Debounces changes to a single rebuild (300ms quiet window — match air's current `delay = 1000` if necessary).
3. On change:
   - Run `go build -o ./tmp/solidping.next .` while the existing `./tmp/solidping` keeps serving traffic.
   - If the build succeeds: send `SIGINT` to the running child, wait up to 2s, `SIGKILL` if needed, then `os.Rename("tmp/solidping.next", "tmp/solidping")` and start the new child with the same args/env that air uses today.
   - If the build fails: log the compiler output, leave the running child untouched, delete `tmp/solidping.next`. **No swap, no kill.**
4. Forwards SIGINT/SIGTERM to the child cleanly so `Ctrl+C` shuts down the dev loop.

Use the same env/args that the current `.air.toml` line 15 builds:

```
SP_REDIRECTS='/dash0:localhost:5174/dash0,/status0:localhost:5175/status0' ./tmp/solidping serve
```

These should be passed via the parent process env / args; do not hardcode in `devloop`.

## Caveat: port 4000 conflict

A naive build-then-kill still has a brief overlap where the old child holds port 4000 and the new child tries to bind it. Two acceptable resolutions:

**Option A (simpler, recommended):** sequence the swap as `signal old → wait for exit → start new`. The old child must release the port before the new one starts. Downtime is then bounded by graceful-shutdown time of the HTTP server (typically <100ms), not by build time. This is the spec's default.

**Option B (truly zero-downtime):** add `SO_REUSEPORT` to the listener in `internal/app/server.go`. New child can bind while old is draining. More invasive, affects production code paths, **explicitly out of scope** for this spec — call it out only if you find it required.

Implement Option A. Verify the reload window is sub-second instead of multi-second.

## Files to add / change

### Add `server/cmd/devloop/main.go`

A single-file Go program (~150 LOC). Uses `github.com/fsnotify/fsnotify` (already a transitive dep — check `server/go.mod`; if not present, `go get` it).

Suggested structure:

```go
// Package main runs `go build` on file changes, then atomically swaps the
// running binary. Replaces `air` for the backend dev loop because air kills
// the running process before building, which forces a multi-second downtime
// window on every save. See specs/done/.../zero-downtime-dev-reload.md.
package main

func main() {
    // 1. Initial build + start
    // 2. Set up fsnotify recursive watcher with the exclude list above
    // 3. Debounce loop:
    //    - On burst end: build to tmp/solidping.next
    //    - If success: stop child (SIGINT, wait, SIGKILL fallback), rename, start
    //    - If failure: print stderr, leave running child alone
    // 4. Trap SIGINT/SIGTERM, forward to child, exit
}
```

Keep it minimal. No flags, no config file — read environment + a couple of constants. The program is dev-only, never shipped, never tested by users beyond the team.

### Change `Makefile` lines 125 and 132

Replace:

```make
@cd $(BACK_DIR) && SP_REDIRECTS="/dash0:localhost:5174/dash0,/status0:localhost:5175/status0" air 2>&1 | tee $(CURDIR)/$(LOG_DIR)/backend.log
```

with:

```make
@cd $(BACK_DIR) && SP_REDIRECTS="/dash0:localhost:5174/dash0,/status0:localhost:5175/status0" go run ./cmd/devloop 2>&1 | tee $(CURDIR)/$(LOG_DIR)/backend.log
```

(Same env, same `tee`, just swap the watcher.) Apply the same change to `dev-test` (line 132).

### Update `server/.air.toml`

Leave it in place but add a top-of-file comment that `make dev` no longer uses it, and that it's kept around for anyone running `air` directly. Or delete it — the team doesn't appear to invoke air outside the Makefile (`grep -rn '\bair\b' Makefile server/` returns only the two lines we just changed). Prefer **delete** to avoid confusion; if kept, mark it deprecated.

### Update `server/CLAUDE.md`

The line "**Run development server**: `make run` or `make air` (with hot reload using air)" needs to drop the air mention or be reworded to reference the new dev loop. There is no `make air` target in the Makefile today, so this line is already partly stale.

### Update `CLAUDE.md` (root)

The "Development Workflow" note about waiting 5s for build is still accurate for the *first* startup but no longer for incremental reloads — the new loop is bounded by graceful shutdown (sub-second). Adjust the wording.

## Out of scope

- Production reload behaviour. The application binary is unchanged; this is a dev-tool replacement.
- Test mode (`SP_RUNMODE=test`) gets the new loop too — no special-casing.
- Dash/status0 frontends. They have their own bun/vite hot reload and are unaffected.
- Recovering from a child that crashes on its own (panic mid-request). For now: print the exit status and wait for the next file change. Keeping it simple.
- `SO_REUSEPORT` (see Option B above).

## Verification

1. Confirm air's behaviour today: edit a `.go` file under `server/`, time how long `curl -s http://localhost:4000/api/mgmt/health` returns "connection refused" before recovering. Expect 1–5 seconds.
2. Apply this spec.
3. Run `make dev-test`. Confirm initial startup works and the dev loop logs "watching server/...".
4. Edit the same `.go` file. The reload window should be <1s; `curl` should briefly fail (or not at all if shutdown is graceful) and recover.
5. Introduce a deliberate syntax error. The dev loop should log the compiler error, the running server should keep responding, no swap.
6. Fix the error. Reload should succeed, server keeps responding throughout.
7. `Ctrl+C` cleanly shuts down both the watcher and the child.
8. `make build`, `make test`, `make lint-back` all still pass — `cmd/devloop` is excluded from the production build and is itself a tiny `main` package, so add it to whatever exclusion lists are necessary if lint complains.

## Implementation Plan

1. Verify `github.com/fsnotify/fsnotify` is reachable as a direct dep. `go get github.com/fsnotify/fsnotify` if not.
2. Write `server/cmd/devloop/main.go` per the structure above. Keep it minimal — fsnotify watcher, 300ms debounce, build-to-`.next`, swap on success, leave alone on failure.
3. Update `Makefile` `dev` and `dev-test` targets to call `go run ./cmd/devloop` instead of `air`.
4. Delete `server/.air.toml` (or keep with a deprecation comment).
5. Update `server/CLAUDE.md` and root `CLAUDE.md` to reflect the new dev loop.
6. Run the verification steps.
