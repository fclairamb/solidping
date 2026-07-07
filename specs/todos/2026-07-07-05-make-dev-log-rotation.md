# `make dev` log files grow unboundedly — devloop supervises the dev processes and rotates their logs

## Problem

All four dev targets pipe each process's combined output through plain `tee`
into `logs/` (Makefile:242–268 and the `dev-back` target):

```make
@cd $(DASH0_DIR) && bun run dev 2>&1 | tee $(CURDIR)/$(LOG_DIR)/dash0.log &
@cd $(STATUS0_DIR) && bun run dev 2>&1 | tee $(CURDIR)/$(LOG_DIR)/status0.log &
@cd $(BACK_DIR) && ... go run ./cmd/devloop 2>&1 | tee $(CURDIR)/$(LOG_DIR)/backend.log
```

`tee` truncates at start but then writes without any bound for the lifetime
of the session. The documented workflow is to leave `make dev` running for
days (hot reload via devloop), and the backend logs every check execution,
scheduler activity, and HTTP request — with sub-minute check periods,
`logs/backend.log` reaches hundreds of MB to multi-GB over a long session.
`dash0.log`/`status0.log` grow slower (vite/HMR chatter) but are equally
unbounded.

Consequences:

- silent disk consumption in a gitignored directory nobody looks at
  (`logs/` already accumulates stray multi-MB files);
- huge files are a hazard for the tools pointed at them — `grep`, editors,
  and Claude Code sessions reading logs to debug;
- no history across restarts: the next `make dev` truncates the previous
  session's log, so today you get *either* unbounded growth *or* nothing.

Two adjacent problems with the same root (the Makefile, not a program, owns
process wiring):

- **Orphaned frontends.** The two `bun run dev` pipelines are backgrounded
  with `&` and nothing owns them; that's why every dev target depends on a
  port-based `kill` target (Makefile:58–61) to reap survivors of the
  previous session.
- **`server/cmd/devloop/` was gitignore-trapped.** `server/.gitignore` had
  an unanchored `devloop` entry (meant for a stray `go build` binary) that
  also matched the `cmd/devloop` directory — `main.go` was tracked only
  because it was added despite the rule, and any *new* file in that package
  (exactly what this spec adds) would have been silently ignored. Fixed
  alongside this spec by anchoring the binary entries (`/back`, `/devloop`,
  `/server`).

devloop already supervises the backend child but just inherits stdout/stderr
(`server/cmd/devloop/main.go:148–149`). It is the natural owner of dev-time
process supervision and log redirection — no separate pipe-sink tool, no
`tee`, no `&`.

## Proposal

### A. Rotating log writer inside `cmd/devloop`

A small stdlib-only writer (e.g. `server/cmd/devloop/rotate.go` — the repo
has no log-rotation dependency and doesn't need one):

- appends to a target file and rotates at a **line boundary** once the
  active file exceeds a max size (default 20 MB): shift `backend.log.1 →
  backend.log.2`, `backend.log → backend.log.1`, drop anything beyond the
  backup count (default 2), reopen. Worst case per stream ~60 MB, ~180 MB
  for the whole `logs/` dir regardless of session length;
- truncates the active file when devloop starts, matching today's `tee`
  semantics of "the log is this session" — the rotated `.1` keeps the tail
  of the previous session as a bonus;
- degrades gracefully: if the file can't be opened or written (disk full,
  permissions), warn once on stderr and keep the terminal stream alive —
  file logging must never take down the dev loop.

Everything devloop emits or relays goes through `io.MultiWriter(stdout,
rotatingFile)`: its own `[devloop]` lines, `go build` failure output
(build errors in the log file matter when debugging a session after the
fact), and the backend child's combined stdout/stderr. The rotating writer
is shared across rebuild/restart cycles — one continuous stream per file,
as with `tee` today.

### B. devloop supervises the frontend dev servers too

Generalize the existing `supervisor` so devloop can run auxiliary child
processes that are *not* part of the rebuild loop — the two `bun run dev`
servers. Sketch (exact flag shape up to the implementer):

```
go run ./cmd/devloop \
  -log-dir <abs path to logs/> \
  -proc dash0:<abs path to web/dash0>:bun run dev \
  -proc status0:<abs path to web/status0>:bun run dev
```

- Each supervised process gets its own rotating file (`<name>.log`, same
  policy as A) plus raw passthrough to devloop's stdout — interleaved,
  unprefixed, exactly like today's three `tee`s writing one terminal.
  (Children are already piped through `tee` today, so no TTY/color
  regression.)
- On devloop shutdown (Ctrl-C, SIGTERM): stop *all* children with the
  existing SIGINT → grace → SIGKILL sequence. Because devloop outlives its
  children by design, the backend's graceful-shutdown lines land in the log
  — plain `tee` dies with the process group and can lose them.
- If a frontend child exits on its own, log it loudly to terminal + file.
  No auto-restart (parity with today, where a crashed background `bun`
  simply disappears — silently).
- `.go`-change rebuilds keep affecting only the backend child; frontends
  have their own HMR.

This removes the orphan problem at the root: the frontends are children of
a foreground process that reaps them. `make kill` stays as a safety net for
crashed devloops, but stops being load-bearing.

### C. Makefile rewiring

`dev`, `dev-test`, and `dev-saas` collapse to environment setup + **one
foreground devloop invocation** — no `tee`, no `&`. Env vars (`SP_RUNMODE`,
`SP_REDIRECTS`, the SaaS `SP_*` set) keep working unchanged: devloop already
passes `os.Environ()` to the backend child.

`dev-back` (`go run . serve 2>&1 | tee …`) switches to devloop with no
`-proc` flags — it gains rotation *and* hot reload. Backend-only-without-
reload stays available as `make run`. Call this semantic change out in the
PR.

### D. Document it

One line in the root `CLAUDE.md` development-workflow section: dev logs live
in `logs/*.log`, size-rotated (`.1`, `.2` suffixes), all three processes
supervised by `cmd/devloop`. Touch the `make dev` description in
`server/CLAUDE.md` accordingly.

## Out of scope

- Reducing backend log *volume* (log levels, sampling per-check-execution
  logs) — orthogonal; rotation must work regardless of verbosity.
- Time-based rotation, compression of backups, syslog/journald integration.
- Auto-restarting crashed frontend dev servers.
- Ad-hoc log files other tooling drops into `logs/` (`e2e-server*.log`,
  `make-dev.log`) — not wired here.
- Guarding against two concurrent devloop instances (same clobber behavior
  as today's double `tee`; the port bind fails fast anyway).
- Production logging — this is dev-workflow only.

## Acceptance criteria

- Unit tests in `cmd/devloop` for the rotating writer: input several times
  the cap yields an active file ≤ max size (plus one line of slack),
  correctly shifted backups, oldest pruned; truncate-on-start; line-boundary
  rotation (no torn lines across files); open/write failure degrades to
  terminal-only without erroring the loop.
- `make dev` shows a single process tree: both `bun` processes are children
  of devloop (`pstree`/`ps`), and all three `logs/*.log` files are written
  with rotation observable live (lowered cap via flag) without disturbing
  hot reload or terminal output.
- Ctrl-C on `make dev` exits cleanly: no orphaned `bun`/`node` processes
  (`lsof -ti :5174 :5175` empty afterwards), and the backend's shutdown
  lines appear in `backend.log`.
- A failed `go build` after a bad save shows up in `backend.log`, not just
  the terminal.
- After a restart, each `<name>.log` starts fresh and the previous tail is
  in `<name>.log.1`.
- `git check-ignore server/cmd/devloop/<newfile>` matches nothing (gitignore
  fix landed with this spec).
- `make test` and `make lint` green.

## Implementation plan

- [x] Anchor `server/.gitignore` binary entries so `cmd/devloop/` is no
      longer ignored (landed together with this spec).
- [ ] A: rotating writer in `cmd/devloop` (size cap, backups, truncate,
      line-boundary, graceful degradation) + unit tests; route devloop's own
      output, build output, and backend child output through it.
- [ ] B: generalize the supervisor — named auxiliary processes with per-name
      rotating logs, full-tree shutdown, loud child-exit reporting.
- [ ] C: Makefile — `dev`/`dev-test`/`dev-saas` become one foreground
      devloop command; `dev-back` drops its `tee` for devloop with no
      frontends.
- [ ] D: CLAUDE.md notes (root + server) on supervised processes and rotated
      log locations.
- [ ] Verify: rotation live-run with a small cap, Ctrl-C orphan check,
      build-failure-in-log check, `make test`, `make lint`.
