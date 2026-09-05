---
model: sonnet
effort: low
---

# The check-throughput bench targets configured the database with three env vars that do not exist

## Problem

`make bench-checks` set three `SP_DB_*` environment variables that are not
config keys. Every one of them was silently ignored, so the bench never ran
with the topology its Makefile claimed.

**`bench-checks-sqlite` set `SP_DB_DATA_DIR`.** The field is
`DatabaseConfig.Dir`, tagged `koanf:"dir"`
(`server/internal/config/config.go:1406`) — the env var is `SP_DB_DIR`. The
server logged

```
Unrecognized SP_* environment variable is ignored; did you mean another name?
  name=SP_DB_DATA_DIR didYouMean=SP_DB_DIR
```

(`server/internal/envcheck/envcheck.go:52`) and fell back to the default data
directory. The SQLite bench had therefore **never** used its own scratch dir:
`bench-data/sqlite` stayed empty run after run, and successive benchmarks
shared — and reset — whatever database lived at the default path.

**`bench-checks-postgres` set two more of the same.** Neither
`SP_DB_EMBEDDED_DIR` nor `SP_DB_PORT` exists in `DatabaseConfig`:

- The embedded server's data directory is not caller-configurable at all.
  `embeddedpg.Start` creates it with `os.MkdirTemp`
  (`server/internal/db/postgres/embeddedpg/embeddedpg.go:124`), and the doc
  comment on `NewEmbedded` is explicit that "the directory itself, its owner
  marker, the startup sweep, and the parent-death watchdog are all owned by
  the embeddedpg package; callers no longer manage their own data directory"
  (`server/internal/db/postgres/postgres.go:233`).
- The port is the hardcoded constant `embeddedPostgresPort = 5433`
  (`server/internal/app/server.go:149`), passed straight into
  `postgres.New`. The Makefile's `BENCH_PG_PORT ?= 5435` was a knob wired to
  nothing — the embedded server had always listened on 5433.

The failure mode is quiet by construction: the warning is the only signal, it
is at WARN level in a log the bench redirects to a file, and the run then
succeeds and prints a plausible report. Nothing downstream notices.

Found while implementing spec `2026-09-04-01` (precise RSS measurement). The
same mistake had been made and fixed in the new `server/cmd/membench`
harness, which now correctly sets `SP_DB_DIR`
(`server/cmd/membench/main.go:435`).

## Proposal

1. `bench-checks-sqlite`: `SP_DB_DATA_DIR` → `SP_DB_DIR`.
2. `bench-checks-postgres`: drop `SP_DB_EMBEDDED_DIR` and `SP_DB_PORT`. There
   is no correct key to substitute — the embedded server owns both — so
   dropping is the honest fix rather than wiring something new. Drop the
   now-dead `BENCH_PG_PORT` variable and the `mkdir -p $(BENCH_DATA)/pg` that
   only ever created an empty directory.
3. Leave a make-level comment above `bench-checks-postgres` recording *why*
   there is no `BENCH_DATA` directory for that target, so the lines are not
   re-added by the next reader who notices the asymmetry with the SQLite
   target.

Acceptance: after `rm -rf bench-data bench-results`, a short
`make bench-checks BENCH_DURATION=20s` must create the SQLite database file
under `bench-data/`, and neither `bench-results/server-sqlite.log` nor
`bench-results/server-postgres.log` may contain an `Unrecognized SP_*`
warning.

## Outcome

Implemented in commit `40941a06d` (`fix(bench): use the real config keys in
the check-throughput bench targets`), on batch branch `batch/2026-09-03`.

Verified with `make bench-checks BENCH_DURATION=20s BENCH_CHECKS=50` after
clearing both directories:

- `bench-data/sqlite/solidping-test.db` created (1.4 MB), and the server
  logged `Database reset complete path=bench-data/sqlite/solidping-test.db` —
  confirming the scratch dir is now actually in use.
- `grep -c 'Unrecognized SP_'` returned `0` for both
  `bench-results/server-sqlite.log` and `bench-results/server-postgres.log`.
- Both backends completed their runs and wrote their reports.
