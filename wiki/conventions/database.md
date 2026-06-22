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

## Development workflow

During a release cycle developers add scratch migrations as needed (e.g., `002_add_foo.up.sql`). At release time:

1. Capture a golden schema (fresh DB + dump) from the scratch migrations.
2. Hand-author `NNN_vX_Y_Z.up.sql` with the **net final DDL** (not a concatenation — omit columns and tables that were added then removed during the cycle).
3. Delete all scratch migrations.
4. Run the new single migration on a fresh DB, dump schema, diff against golden — must match exactly.
5. Run `make test` to confirm both backends pass.

See `.claude/skills/database.md` for the full SQL style guide.
