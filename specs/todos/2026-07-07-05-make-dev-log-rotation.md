# `make dev` log files grow unboundedly — replace `tee` with a size-rotating tee

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

devloop itself just inherits stdout/stderr
(`server/cmd/devloop/main.go:148–149` — `cmd.Stdout = os.Stdout`), so the
`tee` in the Makefile is the **only** file-writing point. Fixing the pipe
sink fixes every stream.

## Proposal

### A. New tool `server/cmd/rotatee` — a rotating `tee`

A small stdlib-only Go program (the repo has no log-rotation dependency and
doesn't need one — the logic is ~80 lines):

- reads stdin, echoes every chunk to stdout **unmodified and unbuffered**
  (the live terminal experience of `make dev` must not change);
- simultaneously appends to `-out <path>`;
- when the active file exceeds `-max-size-mb` (default 20), rotates at a
  line boundary: shift `backend.log.1 → backend.log.2`, `backend.log →
  backend.log.1`, drop anything beyond `-max-backups` (default 2), reopen a
  fresh active file. Worst case per stream: ~60 MB (1 active + 2 backups),
  ~180 MB for the whole `logs/` dir regardless of session length.

Behavior requirements:

- `-truncate` (used by the Makefile): start the active file empty, matching
  today's `tee` semantics of "the log is this session" — rotated backups
  keep the tail of the previous session as a bonus.
- Exit on stdin EOF. `make kill` works by port (Makefile:58–61), so when a
  writer dies its pipe closes and rotatee exits on its own — no orphans, no
  changes to `kill` needed.
- Ignore SIGINT/SIGTERM and keep draining until EOF: on Ctrl-C the shell
  signals the whole foreground group, and plain `tee` dies immediately,
  sometimes losing the child's graceful-shutdown lines. Draining to EOF
  captures them.
- Never break the pipeline on file errors: if the log file can't be opened
  or written (disk full, permissions), warn once on stderr and keep the
  stdout passthrough alive.

### B. Wire it into the Makefile

In `dev`, `dev-test`, `dev-saas`, and `dev-back`: build the tool once at
target start (`cd $(BACK_DIR) && go build -o tmp/rotatee ./cmd/rotatee`,
~1 s, cached), then replace each `tee $(CURDIR)/$(LOG_DIR)/<name>.log` with:

```make
$(CURDIR)/$(BACK_DIR)/tmp/rotatee -truncate -out $(CURDIR)/$(LOG_DIR)/<name>.log
```

A prebuilt binary (not `go run`) because the frontend pipes run from
`web/dash0`/`web/status0` where there is no Go module, and three concurrent
`go run` invocations add startup latency to every `make dev`.

### C. Document it

One line in the root `CLAUDE.md` development-workflow section: dev logs live
in `logs/*.log`, size-rotated (`.1`, `.2` suffixes) — so sessions debugging
via logs know where the tail of older output went. Mention that ad-hoc
long-running redirects (side-car E2E servers etc.) can reuse
`server/tmp/rotatee` instead of `> file`.

## Out of scope

- Reducing backend log *volume* (log levels, sampling per-check-execution
  logs) — orthogonal; rotation must work regardless of verbosity.
- Time-based rotation, compression of backups, syslog/journald integration.
- Ad-hoc log files other tooling drops into `logs/` (`e2e-server*.log`,
  `make-dev.log`) — they can adopt rotatee but aren't wired here.
- Production logging — this is dev-workflow only.

## Acceptance criteria

- Unit tests for rotatee: feeding input several times the cap yields an
  active file ≤ max size (plus one line of slack), correctly shifted
  backups, oldest pruned; stdout passthrough is byte-identical to the
  input; file-open failure still passes input through to stdout.
- `make dev` runs all three streams through rotatee; with a lowered cap
  (flag), rotation is observable live without disturbing hot reload or the
  terminal output.
- Ctrl-C on `make dev` exits cleanly, `pgrep -f rotatee` is empty
  afterwards, and the backend's shutdown lines appear in `backend.log`.
- After a restart, `backend.log` starts fresh (truncate semantics
  preserved) and the previous tail is in `backend.log.1`.
- `make test` and `make lint` green.

## Implementation plan

- [ ] A: `server/cmd/rotatee` — flags (`-out`, `-max-size-mb`,
      `-max-backups`, `-truncate`), line-boundary rotation, EOF exit,
      signal-ignore, degrade-to-stdout on file errors; unit tests.
- [ ] B: Makefile — build rotatee once per dev target, swap the four `tee`
      invocations (`dev`, `dev-test`, `dev-saas`, `dev-back`).
- [ ] C: CLAUDE.md note on rotated log locations.
- [ ] Verify: long-run simulation with a small cap, Ctrl-C behavior,
      `make test`, `make lint`.
