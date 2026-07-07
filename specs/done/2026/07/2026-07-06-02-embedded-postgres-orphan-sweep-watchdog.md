# Embedded Postgres tests: orphaned instances survive SIGKILL — add startup sweep + parent-death watchdog

## Problem

Integration tests boot real PostgreSQL servers via
`github.com/fergusstrange/embedded-postgres`. When the test process exits
normally, `defer`/`t.Cleanup` stop the server and remove the data dir. But
when the process is killed hard — a Claude Code background shell being
stopped, a CI timeout, `kill -9`, an IDE test-runner abort — none of that
runs, and:

- the postgres server keeps running forever (embedded-postgres starts it via
  `pg_ctl`, which daemonizes it — postgres is **not** a direct child of the
  test process and is not killed with it);
- the data dir under `$TMPDIR` (`/var/folders/...` on macOS) leaks, ~40MB
  each.

Observed on 2026-07-06: `ps aux | grep solidping-scenario-pg` showed **12
orphaned postgres instances** accumulated over two days of development.

A secondary symptom: several suites use fixed ports (5435, 5447, 15437,
headroom-test ports). An orphan still holding its port makes the next run of
that suite fail or skip with "port already in use" — the leak causes flakes,
not just resource waste.

### Call-site inventory (all embedded-postgres startup paths)

Everything funnels through `postgres.NewEmbedded`
(`server/internal/db/postgres/postgres.go:188`) **except** the scenario
suite, which calls the library directly:

| Call site | Data dir today | Port |
|---|---|---|
| `test/integration/scenario/main_test.go:38` — direct `embeddedpostgres.NewDatabase` | `os.MkdirTemp("", "solidping-scenario-pg-*")`, `defer os.RemoveAll` | random 5450–5950 |
| `internal/notifier/postgres_test.go:23,331` → `NewEmbedded` | `os.MkdirTemp("", "pg-notifier-test-*")` / `"pg-notifier-factory-test-*"` | free-port helper |
| `internal/realtime/multireplica_test.go:47` → `NewEmbedded` | `os.MkdirTemp("", "realtime-multireplica-*")` | free-port helper |
| `internal/db/service_test.go:1055` → `NewEmbedded` | `os.MkdirTemp("", "postgres-test-*")` | fixed 5435 |
| `internal/jobs/jobsvc/reapstuck_test.go:247` → `NewEmbedded` | `os.MkdirTemp("", "jobsvc-reap-pg-*")` | fixed 5447 |
| `internal/checkworker/checkjobsvc/service_postgres_test.go:41` → `postgres.New{Embedded: true}` | `t.TempDir()` | fixed 15437 |
| `internal/scheduling/costdist/costdist_postgres_test.go:32` → same | `t.TempDir()` | fixed |
| `internal/handlers/results/service_neighbors_postgres_test.go:33` → same | `t.TempDir()` | fixed |
| `internal/db/postgres/postgres_headroom_postgres_test.go:52` → same | `t.TempDir()` | several fixed |

Neither `os.MkdirTemp` + `defer` nor `t.TempDir()` survives SIGKILL, and in
no case does the postgres *process* get reaped.

### Why the obvious fixes don't work

- **A goroutine watching `os.Getppid()`** dies with the process on SIGKILL —
  goroutines are not processes.
- **Making postgres a direct child** so it dies with the parent isn't
  available: embedded-postgres drives `pg_ctl`, which double-forks.
- **`t.Cleanup` / `defer`** is exactly what SIGKILL bypasses.

The reliable pattern needs two independent layers: a **separate watchdog
process** (survives the parent's SIGKILL, reaps postgres when the parent
dies) and a **startup sweep** (catches anything the watchdog missed —
watchdog itself killed, machine rebooted mid-test leaving stale dirs, etc.).

## Proposal

### A. Shared helper package `internal/db/postgres/embeddedpg` (or `internal/testsupport/embeddedpg`)

One small package that owns the full lifecycle. API sketch (exact shape up to
the implementer):

```go
type Options struct {
    Suite           string            // e.g. "scenario", "notifier" — becomes part of the dir name
    Port            uint32
    Database, Username, Password string
    StartParameters map[string]string // scenario overrides shared_buffers etc.
}
func Start(opts Options) (*Instance, error) // Instance has DSN(), DataDir(), Stop()
```

Responsibilities:

1. **Owns the data dir.** Creates
   `os.MkdirTemp(os.TempDir(), "solidping-embedded-pg-<suite>-*")` itself —
   callers stop passing `t.TempDir()` or their own `MkdirTemp` dirs. A single
   unified prefix is what makes the sweep reliable.
2. **Writes an owner marker** `owner.json` in the data dir before starting
   postgres: `{ownerPid, startedAt, suite}`. The postgres PID itself needs no
   bookkeeping — postgres writes it as the first line of `postmaster.pid` in
   the data dir.
3. **Runs the startup sweep** (step B) once per process (`sync.Once`) before
   starting a new instance.
4. **Spawns the watchdog** (step C) after postgres is up.
5. **`Stop()`** stops postgres (existing `pg.Stop()`), signals the watchdog
   to exit (it exits on its own when it sees the data dir gone), and removes
   the data dir.

### B. Startup sweep — reap orphans from previous runs

Before starting a new instance, scan `os.TempDir()` for directories matching:

- the new unified prefix `solidping-embedded-pg-*`, and
- the legacy prefixes for one transition period: `solidping-scenario-pg-*`,
  `pg-notifier-test-*`, `pg-notifier-factory-test-*`,
  `realtime-multireplica-*`, `postgres-test-*`, `jobsvc-reap-pg-*`.

For each candidate dir:

1. Read `owner.json`. If the owner PID is still alive (`syscall.Kill(pid, 0)`
   succeeds), skip — it's a concurrently running suite.
2. If the owner is dead (or `owner.json` is absent — legacy dir — and the dir
   is older than ~1 hour by mtime): read `postmaster.pid`, and if that PID is
   alive **and its command line references this data dir** (guard against PID
   reuse — check via `ps -o command= -p <pid>` output containing the dir
   path), SIGKILL it (kill the process group per `postmaster.pid` semantics,
   or the PID directly and let its children die with the postmaster).
3. `os.RemoveAll(dir)`.

Fallback pass: `pgrep -f 'solidping-embedded-pg|solidping-scenario-pg'` to
catch postgres processes whose data dir was already deleted; verify each
match's command line before killing. Both `pgrep` and `ps` exist on macOS and
Linux; the sweep is best-effort and must never fail the test run (log to
stderr and continue).

This alone fixes the port-collision flakes: a suite with a fixed port sweeps
its own predecessor before binding.

### C. Parent-death watchdog — prevent new leaks

After postgres starts, spawn a small **detached** monitor process (survives
the test process's SIGKILL):

```sh
sh -c 'while kill -0 $OWNER_PID 2>/dev/null; do sleep 5; done;
       kill $PG_PID 2>/dev/null; sleep 2; kill -9 $PG_PID 2>/dev/null;
       rm -rf "$DATA_DIR"'
```

- Start it with `exec.Command` + `Setsid: true` (own session, not the test's
  process group) and stdio detached, so nothing tethers it to the parent.
- It polls the owner PID; when the owner dies *for any reason* (SIGKILL
  included), it terminates postgres (TERM then KILL) and removes the data
  dir, then exits.
- Normal shutdown: `Stop()` stops postgres and removes the dir first; the
  watchdog's subsequent kill/rm are no-ops on missing PID/dir. To avoid a
  stray 5s-lingering `sh` per test run, `Stop()` may also kill the watchdog
  PID directly (it recorded it).
- PID-reuse window (owner PID recycled while watchdog polls) is acceptable
  for test infra; the sweep in B is the safety net.
- Windows: not a target (dev = macOS, CI = Linux). Guard with
  `runtime.GOOS`; on unsupported platforms skip the watchdog and rely on the
  sweep.

### D. Migrate all call sites

- `postgres.NewEmbedded` (`postgres.go:188`): route through
  `embeddedpg.Start` internally. The `dataDir` parameter becomes a suite
  label (or is dropped); `Config.EmbeddedDir` on the `postgres.New` path
  likewise stops being caller-supplied — if kept for compatibility, a
  non-empty value still gets the marker + watchdog treatment.
- `test/integration/scenario/main_test.go`: replace the direct
  `embeddedpostgres.NewDatabase` block with `embeddedpg.Start` carrying its
  custom `StartParameters` (`shared_buffers: 256kB`, `max_connections: 50`)
  and DB name; keep the existing graceful-skip behavior when startup fails.
- The four `Config{Embedded: true, EmbeddedDir: t.TempDir()}` suites
  (checkjobsvc, costdist, results neighbors, headroom) and the four direct
  `NewEmbedded` suites (notifier ×2, realtime, db service, jobsvc reapstuck):
  drop their own dir creation.

## Out of scope

- Replacing embedded-postgres with testcontainers (heavier, needs Docker in
  every environment the tests run in today).
- Moving the fixed-port suites to random ports — worthwhile, but the sweep
  removes the pain and the ports are baked into several test constants.
- The embedded-postgres binary cache (`~/.embedded-postgres-go`) — shared,
  not leaked per-run.
- Windows support for the watchdog.

## Acceptance criteria

- Manual kill test: start the scenario suite, `kill -9` the test process
  mid-run → within ~10s the postgres instance is gone (`pgrep -f` empty) and
  its data dir removed by the watchdog.
- Sweep test: fabricate a stale dir (marker with a dead owner PID) → next
  `embeddedpg.Start` removes it; a dir whose owner PID is alive is left
  untouched. Unit-testable without booting postgres.
- Concurrent suites (e.g. `make test` running scenario + notifier +
  checkjobsvc in parallel) don't sweep each other — live-owner check proven
  by test.
- No caller outside `embeddedpg` creates an embedded data dir anymore
  (`grep MkdirTemp`/`t.TempDir()` near embedded startup comes back empty).
- The 12 currently orphaned instances are killed and their dirs removed
  (one-off, part of verification: `pgrep -f solidping-scenario-pg`, kill,
  remove matching `/var/folders/.../solidping-scenario-pg-*`).
- `make test` and `make lint` green; normal runs leave no `solidping-*-pg-*`
  dirs and no stray `sh` watchdogs behind (`pgrep -f` clean after the run).

## Implementation plan

- [ ] A: create `embeddedpg` package — dir ownership, `owner.json` marker,
      `Start`/`Stop`, options for suite name, port, DB name, start params.
- [ ] B: startup sweep (unified + legacy prefixes, live-owner check,
      `postmaster.pid` + command-line-verified kill, `pgrep -f` fallback);
      unit tests with fabricated dirs/markers.
- [ ] C: detached watchdog process (Setsid, TERM→KILL→rm -rf loop),
      killed/ignored cleanly on normal `Stop()`.
- [ ] D: rewire `postgres.NewEmbedded` + `Config.EmbeddedDir` path; migrate
      scenario `main_test.go` and the eight test suites off self-managed
      dirs.
- [ ] Verify: manual SIGKILL test (scenario suite), `make test`,
      `make lint`; kill the 12 existing orphans and confirm
      `pgrep -f solidping` shows no embedded postgres leftovers.
