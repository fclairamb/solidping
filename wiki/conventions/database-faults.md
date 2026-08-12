# Database faults: transient vs structural

Not every database error deserves a retry. A dropped connection, a deadlock, a
saturated pool, "the database system is starting up" — those come back on their
own, and the right answer is to back off and try again. But when the *schema* is
gone, retrying is theatre: migrations only run at startup, so nothing inside a
live process can ever repair a missing table.

`server/internal/db/dbfault` is where that distinction is made, once, for every
component that talks to the database.

## The incident this comes from

A `solidping-e2e serve` process ran for **seventeen hours** with every job query
failing on:

```
SQL logic error: no such table: jobs (1)
```

It never stopped, never reported unhealthy, and never exited. It logged the same
error until the host disk was full. Spec `2026-08-12-04` bounded the *volume*
(exponential backoff + log collapsing); spec `2026-08-12-05` — this document —
is about the process staying alive at all.

### Root cause, reproduced

The `jobs` table existed at boot: `main.go` calls `server.Initialize(ctx)`,
which runs migrations, and the process started fine. The table disappeared
*underneath a live process*.

The mechanism is reproduced by
`TestSchemaVanishesWhenDatabaseFileIsDeleted`
(`server/internal/db/sqlite/schema_vanished_test.go`), which uses the real
production constructor and asserts the incident's exact error string:

1. **The SQLite database file is unlinked while the server runs.** Any of: an
   e2e teardown, a second server booting with `SP_DB_RESET=true` against the
   same `SP_DB_DIR` (`resolveDatabasePath` removes the `.db`, `-wal` and `-shm`
   files), or a temp-directory sweeper reclaiming the path.
2. **Nothing appears to happen.** The connection already open holds the
   unlinked inode, so queries keep succeeding and no error is logged. This is
   why the fault has no visible starting point in the logs.
3. **The pool opens a fresh connection** — because one was discarded after an
   error, reaped, or because the pool briefly drained — and the DSN's
   `mode=rwc` **silently creates a brand-new, empty database file** at that
   path.
4. **Every query fails from then on**, forever, with `no such table`. The file
   exists and is 0 bytes, so nothing at the filesystem level looks broken
   either.

Nothing in the repository creates the `sidecar.log` the incident's output went
to, so the process itself came from an ad-hoc command rather than committed
code; the committed Playwright harness uses Postgres. The harness bugs it *did*
have are listed below.

## What is classified as structural

| Backend | Signal | Condition |
|---|---|---|
| PostgreSQL | SQLSTATE `42P01` | `undefined_table` |
| PostgreSQL | SQLSTATE `42703` | `undefined_column` |
| PostgreSQL | SQLSTATE `42704` | `undefined_object` |
| PostgreSQL | SQLSTATE `42883` | `undefined_function` |
| PostgreSQL | SQLSTATE `3D000` | `invalid_catalog_name` — the database itself is gone |
| PostgreSQL | SQLSTATE `3F000` | `invalid_schema_name` |
| SQLite | result code `11` | `database_corrupt` |
| SQLite | result code `26` | `not_a_database` |
| SQLite | result code `1032` | `database_file_moved` (`SQLITE_READONLY_DBMOVED`) |
| SQLite | code `1` + `no such table/column/index/view/trigger:` | the matching `undefined_*` |

SQLSTATEs are read from **both** PostgreSQL drivers in the tree: bun's
`pgdriver` (the ORM) and `lib/pq` (the LISTEN/NOTIFY notifier, the postgres
checker).

SQLite is the awkward one: it has **no dedicated result code for a missing
table** — `no such table` is the generic `SQLITE_ERROR (1)`, the same code as a
syntax error. So the code narrows the error to a logic error and the message
names the missing object. Both driver wordings are covered and tested (the
pure-Go `modernc` driver renders `SQL logic error: no such table: jobs (1)`,
the cgo `mattn` driver `no such table: jobs`). Keeping that message rule in
*one* place is precisely the point: a string match scattered across callers is
what silently stops matching when a driver rewords itself.

**Everything else is transient.** The default is retry, so an unrecognised
error behaves exactly as it did before this existed — `08xxx` connection
exceptions, `40001`/`40P01`, `53300`, `55P03`, `57P01`/`57P03`, `SQLITE_BUSY`,
`SQLITE_LOCKED`, `SQLITE_IOERR`, dropped sockets, context cancellation.

## What happens when a structural fault is seen

`dbfault.Latch` is the terminal-action hook. The job runner and the check
worker's fetcher pass every error through it:

- **One log line**, `dbfault.LogMessage`, naming the fault (`reason`,
  `backend`, `code`, `component`). Later occurrences are silent — the process
  is already on its way out. This is the line to grep for; an ordinary outage
  never produces it.
- **The runner returns** instead of backing off, so it stops hammering a
  database that can never answer.
- **The process shuts down gracefully.** The latch cancels the context
  `Server.Start` runs on, so HTTP drains and runners get their normal timeout —
  not an `os.Exit` from a random goroutine. `Start` then returns the fault, so
  the process exits non-zero and the supervisor restarts it, which **re-runs
  migrations and may fix the condition outright**.
- **`/api/mgmt/health` answers `503`** with `{"status":"unhealthy","fault":
  "undefined_table"}` for the shutdown window. The incident's worst property
  was a process answering `ok` while every query failed.

Visible in seconds, terminal in seconds — instead of a silent seventeen-hour
spin.

The classification is reactive (errors are classified as they occur) rather
than an active probe. With the classification centralised, a probe would only
buy earlier detection on a completely idle server, at the cost of a periodic
query.

### Composition with the backoff

Backoff (spec `2026-08-12-04`) bounds a repeated failure's **volume**;
fail-fast bounds its **duration**. They are two halves of the same fix:
backoff without fail-fast is a silent, useless worker; fail-fast without
backoff means the next unclassified error still floods.

### Adding a caller

> [!WARNING]
> **Only errors from SolidPing's *own* database may be reported to the latch.**
> Never report an error that came from a database SolidPing is *probing*, no
> matter how it classifies. `Report` shuts the server down.
>
> This is not hypothetical: `internal/checkers/checkpostgres` runs `lib/pq`
> against **customer** databases as part of normal monitoring, and a customer's
> Postgres returning `42P01 undefined_table` is a perfectly ordinary check
> result — their schema, their problem, reported as a failed check. Routed to
> `Report`, it would take *our* server down because *their* table is missing.
> The same applies to any future checker that speaks a database protocol.
>
> The rule is about provenance, not about the error: the classifier answers
> "is this schema gone?", and only the caller knows *whose* schema it was. The
> latch is installed in exactly two places today — the job runner and the check
> worker's fetcher, both of which only ever touch our own database
> (`internal/app/server.go`). Keep it that way.

For a component that does talk to our own database: give it a `*dbfault.Latch`
field and call `latch.Report(ctx, err, "component", "...")` on its error path;
return when it reports `true`. A nil latch reports everything as non-terminal,
so tests, the integration harness and agent mode need no nil checks and keep
their previous behaviour. If you only want the classification — including for a
probed database, where it is a useful label and nothing more — use
`dbfault.IsStructural`, which has no side effects.

## Not being orphaned in the first place

The incident's process had **PPID 1** and outlived its session by seventeen
hours. Two fixes:

- **`SP_EXIT_WITH_PARENT=true`** (`server.exit_with_parent`) makes the server
  shut down when the process that started it disappears, rather than being
  adopted by init. It is a `getppid()` poll, because `PR_SET_PDEATHSIG` has no
  portable equivalent (macOS has none). Off by default: a normal deployment is
  started *by* a supervisor whose death is not a reason to stop. See
  `server/internal/procwatch`.
- **The Playwright harness** (`web/dash0/e2e/global-setup.ts`) now sets that
  variable on the server it starts, and — the actual harness bug — stops that
  server *before* running `docker compose down` on the failure path. Playwright
  does not run `globalTeardown` when `globalSetup` throws, so a setup failure
  used to destroy the database and leave the server it had just spawned running
  against nothing, forever.
