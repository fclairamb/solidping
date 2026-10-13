---
model: opus
effort: high
---

# An interrupted or failed migration is never marked applied, and an interrupted first boot leaves no database behind

## Problem
Stopping a container during its first start breaks its SQLite database for good. Every later start fails:

```
level=ERROR msg="Failed to auto-migrate credentials" error="split plaintext credentials: list organizations: SQL logic error: no such column: organization.default_escalation_policy_uid (1)"
level=ERROR msg="Application failed" error="split plaintext credentials: ..."
```

Seen 2026-10-13 while testing a Portainer template with `ghcr.io/fclairamb/solidping:latest` (0.38.3): the first container was force-removed about 15 seconds after `docker start`, a fresh container on the same volume then crash-looped with the error above. The baseline migration alone took 13.5 s on that run (`level=WARN msg="slow SQL query" duration=13.565252883s ... consolidated baseline schema (SQLite)`), so the window is wide: anyone who stops a new install early (`docker compose down`, a host reboot, an orchestrator killing a slow pod, a failed health check) hits it. `organization.default_escalation_policy_uid` comes from `server/internal/db/sqlite/migrations/006_v0_5_0.up.sql`, so 006 was recorded as applied without its DDL having run.

**Why.** Both backends build the migrator with bun's defaults: `migrate.NewMigrator(s.db, migrations)` at `server/internal/db/sqlite/sqlite.go:325` and `server/internal/db/postgres/postgres.go:345`. In bun v1.2.18 (`server/go.mod:302`) the default is to mark a migration applied **before** running it (`migrate/migrator.go:215-233` in the module: `if !m.markAppliedOnSuccess { m.MarkApplied(...) }`, then `migration.Up(...)`). So:

- a process killed while migration N runs leaves N recorded in `bun_migrations` with only part of its statements executed;
- a migration that **returns an error** half way is also already recorded, so the next boot skips it silently. This is the same bug without a kill.

None of the migrations is transactional: no file uses bun's `.tx.up.sql` suffix (`migrate/migration.go:121`), so a SQL file runs statement by statement in autocommit, split on `--bun:split`. Three SQLite migrations depend on autocommit on purpose, because `PRAGMA foreign_keys` is a no-op inside a transaction: `005_v0_4_0.up.sql:8-14`, `009_v0_8_0.up.sql:31-39`, `010_v0_10_0.up.sql:57-62`. Wrapping every migration in a transaction is therefore not a blanket fix.

The checksum guard (`server/internal/db/migrationguard`, spec 2026-08-18-02) does not help: it checks that applied files were not rewritten, not that their DDL actually ran.

## Proposal
1. **Mark applied only on success, both backends.** Pass `migrate.WithMarkAppliedOnSuccess(true)` in `server/internal/db/sqlite/sqlite.go:325` and `server/internal/db/postgres/postgres.go:345`. A migration that fails or is interrupted then stays pending and runs again on the next boot. On Postgres, DDL is transactional per statement but the migration as a whole is not, so a rerun can hit "already exists" on the statements that did run: that is a loud startup error with the migration name, which is the goal, instead of a silently missing column.
2. **SQLite first boot is atomic.** When the database file does not exist yet (`resolveDatabasePath`, `server/internal/db/sqlite/sqlite.go:136-155`), build it under a temporary name in the same directory (e.g. `solidping.db.init`), run `migrate` against it, close it (checkpoint the WAL so no `-wal`/`-shm` is left), then `os.Rename` it to the real name and reopen. On start, delete any leftover `*.init` (and its `-wal`/`-shm`): it can only come from an interrupted first boot. `New` (`sqlite.go:158`) opens the pool and runs `VACUUM` (`sqlite.go:207`) before `Initialize` (`sqlite.go:303`); the first-boot path must happen before that open, or `New` must defer opening the real file until migrations are done. In-memory databases are unaffected (`initializeInMemory`, `memtemplate.go`).
3. **Existing SQLite databases: back up before pending migrations.** In `migrate` (`sqlite.go:314`), when the file already existed and `migrations.Unapplied()` (after `migrator.Init`) is not empty, write `VACUUM INTO '<dir>/solidping.db.pre-<first pending name>.bak'` before `migrator.Migrate`. Keep only the most recent backup. On a migration error, log the backup path in the error so the operator can restore it. (See Open questions.)
4. **New migrations are transactional by default.** Document in `server/internal/db/sqlite/migrations/` and `server/internal/db/postgres/migrations/` (README, or the repo's CLAUDE.md section on migrations, whichever already holds the conventions) that new files are named `NNN_vX_Y_Z.tx.up.sql` unless they need autocommit (SQLite `PRAGMA foreign_keys`, Postgres `CREATE INDEX CONCURRENTLY`), and why. Bun names a migration by its numeric prefix only (`migrate/migrations.go:165-178`, regexp `^(\d{1,14})_([0-9a-z_\-]+)\.`), so the `.tx` suffix does not change the recorded name. Do **not** rename the existing 27 files: they are applied on every install, and step 2 already makes a fresh install atomic.
5. **Clear error for databases already broken by this bug.** When a query fails with `no such column` / `no such table` right after `Migrate` reported nothing pending, the startup error should say the database looks half-migrated and point to the backup or to `solidping migrate repair` docs, not only print the SQL error. Keep it to the error message; no automatic repair of an unknown state.

## Tests
- `server/internal/db/sqlite/sqlite_test.go` (or a new `migrate_atomic_test.go`): a test migration FS whose second migration fails on its second statement. After `Initialize` returns the error, `bun_migrations` does **not** contain that migration; fixing the FS and calling `Initialize` again applies it. Negative case: with the option removed, the test fails (the migration is recorded).
- Same file: first boot interrupted. Run the first-boot path with a context cancelled during migration (or a migration that blocks until cancelled). Afterwards the real `solidping.db` does not exist and the `.init` file is removed on the next `New`; the next `Initialize` completes and the server's own queries (`list organizations`) work.
- Same file: first boot not interrupted produces exactly one `solidping.db` with no `.init`, `-wal` or `-shm` leftovers from the temp file, and `migrationguard` reconciles cleanly on the next start.
- Backup: an existing database with one pending migration gets one `*.pre-<name>.bak` that opens and contains the pre-migration schema; a database with nothing pending gets no backup; a second upgrade replaces the previous backup.
- `server/internal/db/postgres` tests: the same "fails half way, not recorded, retried" case against the test Postgres.
- Manual, in the PR body: `docker run` a fresh container, `docker rm -f` it 5 seconds in, start a new one on the same volume, `/api/mgmt/health` is 200.

## To verify
- That `resolveDatabasePath` and `New` (`sqlite.go:136-240`) are the only places that open or create the file before `Initialize`, including `--reset` in test/demo mode (`sqlite.go:148`) and the `solidping migrate repair` command.
- Whether `migrator.Init` creates `bun_migrations`/`bun_migration_locks` in the temp file correctly when the file is renamed afterwards (it should: it is just tables in the file).
- Whether the bun migration lock (`bun_migration_locks`) is used here and could also be left held by a killed process; if so, step 1 needs a stale-lock check too.
- Postgres: whether any existing migration would fail on a rerun of a fully applied statement in a way that matters, now that a failed migration is retried (only relevant for migrations that failed in the field).

## Open questions
- **Backup before upgrades (step 3): keep it?** It costs one extra copy of the database on disk during each upgrade. Recommended: yes, keep only the latest one, and skip it with a log line when free disk space is under twice the database size.
- **Postgres in scope now?** Recommended: yes for step 1 (one line, same bug), no for steps 2-3 (Postgres installs are run by people who have their own backups, and a fresh Postgres DB is created by the operator, not by SolidPing).
