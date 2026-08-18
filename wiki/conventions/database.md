# Table conventions
- All entities have a `uid` UUID (external/API)
- All entities except global ones have an `organization_uid` reference
- All tables should be in plural (`organizations`, `users`, `checks`)
- All link tables should have the plural-plural forms (`properties_assets` that links `properties` and `assets`)
- Soft deletes: `deleted_at` timestamp, never hard delete
- Audit trail: `created_at` and `updated_at` timestamps

# Indexes conventions
Indexes should have the name `${table}_${columns}_idx`, the columns should not contain `_uid`.
So for example `incidents_organization`.

# Migrations

## One migration per release

Each release contributes **exactly one migration file** per engine, consolidating all schema changes since the previous release. The naming pattern is:

```
NNN_vX_Y_Z.up.sql   — forward migration (creates all schema for that release)
NNN_vX_Y_Z.down.sql — reverse migration (drops everything; parity only)
```

Example: `001_v0_1_0.up.sql` for v0.1.0.

Migration files live in:
- `server/internal/db/postgres/migrations/` (Postgres)
- `server/internal/db/sqlite/migrations/` (SQLite)

Both directories are always kept in sync and use the same `NNN` sequence number.

## Filename constraints

Bun's migration discovery regex (`[0-9a-z_\-]` only) does **not** allow dots. Use underscores for version separators: `001_v0_1_0`, not `001_v0.1.0`.

## Freeze rule

Migrations already shipped in a released version (`vX.Y.Z` git tag) are **frozen and never rewritten**. Only add new files for new releases.

## Gaps in the `NNN` sequence are fine — never renumber to close one

The sequence may contain holes (at the time of writing, `013` is followed by
`015`: a `014` was added and then withdrawn). **Leave them.** Bun discovers
migrations by numeric prefix and applies whichever are unapplied, in order — a
gap costs nothing.

Renumbering a later migration down into a hole is actively dangerous, because
the hole is only empty *in the source tree*. Databases that ran the withdrawn
migration still carry its number in `bun_migrations`. Slide the next migration
into that number and bun sees it as already applied and **silently skips it** —
the exact failure mode the integrity guard below exists to catch, reintroduced
by hand. On those databases the guard then refuses to boot on the checksum
mismatch, which is correct but means a hand repair before anything runs again.

So: a gap is cosmetic, closing it is a schema bug. Add the next number and move
on.

## Integrity guard: an applied migration is frozen too

The freeze rule above is about *released* migrations. The trap is the pending
one: bun keys an applied migration on its **numeric prefix alone**, so editing
`013_v0_16_0.up.sql` after a dev loop has already run an earlier draft of it
does **not** re-run it. The new statements never execute, `bun_migrations` keeps
claiming 013 is applied, and the database silently lacks whatever the rewrite
added. That has bitten this repository twice — most recently as a missing
`workers.capabilities` column that 404'd region resolution and stopped check
scheduling for two days (spec 2026-08-18-02).

`internal/db/migrationguard` makes that loud. On every boot, before migrating,
it compares a SHA-256 of each applied migration's `.up.sql` against the
checksum recorded in the `migration_checksums` side table:

- **No record** (a database that predates the guard) → the checksum is
  backfilled and the boot proceeds. Existing healthy databases never trip it.
- **Record matches** → normal boot.
- **Record differs** → what happens depends on the guard mode (below).

Only the `.up.sql` half is hashed: a `.down.sql` never runs during a forward
boot, so editing one cannot desync an applied schema. A Go migration would
have no file to hash; `migrationguard.New` accepts declared checksums for
that case (no current occupant).

### Guard mode: `strict` (default) or `warn`

`db.migration_guard_mode` / `SP_DB_MIGRATION_GUARD_MODE` (**multi-word koanf
key — read by the manual `applyMigrationGuardModeEnv` reader, not the
automatic env loader**, same quirk as `db.slow_query_threshold`) picks the
behavior on a checksum mismatch:

- **`strict`** (the default, and the only mode production should run) —
  startup **fails** with an error naming the migration, both checksums, and
  the repair options. Nothing is written to `migration_checksums` while any
  mismatch exists, not even for an unrelated migration awaiting its first
  checksum — a database that is already diverged must not have its record
  quietly updated.
- **`warn`** — the mismatch is logged (`slog.WarnContext`, once per boot) and
  the boot continues. `make dev` / `make dev-test` / `make dev-saas` always run
  this way (set in the root `Makefile`), because editing a comment in an
  already-applied migration during local development is common and must not
  brick the dev database. Warn mode still backfills any migration with no
  recorded checksum at all, even while another migration mismatches — only the
  mismatched row's own checksum is protected from being overwritten, in both
  modes, which is what makes the warning recur every boot until someone
  repairs deliberately.

### Repairing a database the guard rejects

1. **Cosmetic edit** (comment/whitespace only in an already-applied
   migration) — run `solidping migrate repair`. It re-records checksums for
   every applied migration this binary ships (inserting a missing row or
   updating a drifted one to the current file's checksum) **without running
   any migration** — it only ever touches `migration_checksums`.
2. **Development** — reset the database (`SP_DB_RESET=true` in test/demo run
   mode, or delete the SQLite file), or run with
   `SP_DB_MIGRATION_GUARD_MODE=warn` to boot anyway.
3. **Otherwise** — apply the missing statements by hand, then run
   `solidping migrate repair`.

The correct *prevention* is never to edit an applied migration: add a new
numbered one instead, written to be idempotent so it is a no-op on databases
that already got the DDL. (A self-healing `014_v0_17_0` once did this for a
rewritten 013; it was removed before release in favor of repairing the few
affected dev databases by hand — pre-release, hand repair beats shipping a
permanent migration.)

## Development workflow

During a release cycle developers add scratch migrations as needed (e.g., `002_add_foo.up.sql`). At release time:

1. Capture a golden schema (fresh DB + dump) from the scratch migrations.
2. Hand-author `NNN_vX_Y_Z.up.sql` with the **net final DDL** (not a concatenation — omit columns and tables that were added then removed during the cycle).
3. Delete all scratch migrations.
4. Run the new single migration on a fresh DB, dump schema, diff against golden — must match exactly.
5. Run `make test` to confirm both backends pass.

See `.claude/skills/database.md` for the full SQL style guide.
