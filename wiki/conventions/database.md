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
- **Record differs** → startup **fails** with an error naming the migration,
  both checksums, and the two repair routes.

Only the `.up.sql` half is hashed: a `.down.sql` never runs during a forward
boot, so editing one cannot desync an applied schema. Go migrations have no
file to hash and declare their checksum instead (see
`sqlite/gomigrations`).

### Repairing a database the guard rejects

1. **Development** — reset it. `SP_DB_RESET=true` in test/demo run mode, or
   delete the SQLite file. This is almost always the right answer.
2. **Otherwise** — apply the missing statements by hand, then reconcile the
   record with the `UPDATE migration_checksums SET checksum = '<current>' WHERE
   name = '<NNN>';` statement the error message prints verbatim.

The correct *prevention* is never to edit an applied migration: add a new
numbered one instead, written to be idempotent so it is a no-op on databases
that already got the DDL. `014_v0_17_0` is the worked example — it re-applies
013's DDL, guarded per-object, so a desynced database and a correctly-migrated
one end up schema-identical.

## Development workflow

During a release cycle developers add scratch migrations as needed (e.g., `002_add_foo.up.sql`). At release time:

1. Capture a golden schema (fresh DB + dump) from the scratch migrations.
2. Hand-author `NNN_vX_Y_Z.up.sql` with the **net final DDL** (not a concatenation — omit columns and tables that were added then removed during the cycle).
3. Delete all scratch migrations.
4. Run the new single migration on a fresh DB, dump schema, diff against golden — must match exactly.
5. Run `make test` to confirm both backends pass.

See `.claude/skills/database.md` for the full SQL style guide.
