---
name: database
description: Database conventions for SolidPing SQL migrations. Use when creating or modifying migration files, tables, columns, or indexes.
---

# Database Conventions

## Migration file naming and consolidation rule

**One migration file per release.**

- File pattern: `NNN_v<major>_<minor>_<patch>.up.sql` / `.down.sql`  
  Example: `001_v0_1_0.up.sql`, `002_v0_2_0.up.sql`
- `NNN` is a 3-digit zero-padded sequence. Each new release increments it by 1 and re-syncs both engines to the same number.
- **No dots in filenames** — Bun's discovery regex (`migrate/migrations.go`) allows only `[0-9a-z_\-]`; dots silently truncate the name.
- **Freeze-released-migrations**: migrations already shipped in a released version (`vX.Y.Z` git tag) are **frozen and never rewritten**. Only add new migration files for new releases.
- **During a release cycle**: add incremental scratch migrations as usual (`002_add_foo.up.sql`, etc.). At release time, collapse them into a single `NNN_vX_Y_Z.up.sql` and delete the scratch files.
- The `.down.sql` drops all tables in reverse dependency order (parity only; downs are never run in production).

## SQL conventions

### General
- Use `snake_case` for all identifiers (tables, columns, indexes)
- Use lowercase SQL keywords
- Primary keys are always `uid` (uuid for PostgreSQL, text for SQLite)
- All tables have `created_at` and `updated_at` timestamps (except append-only tables like `results` which only have `created_at`)
- Soft-deletable tables have a nullable `deleted_at` column
- Foreign keys use `<referenced_table_singular>_uid` naming (e.g., `organization_uid`, `check_uid`)
- All foreign keys use `on delete cascade` or `on delete set null` — never leave dangling references
- Boolean columns: PostgreSQL uses `boolean`, SQLite uses `integer` (0/1)
- JSON columns: PostgreSQL uses `jsonb`, SQLite uses `text`
- Timestamp columns: PostgreSQL uses `timestamptz`, SQLite uses `text`
- Interval columns: PostgreSQL uses `interval`, SQLite uses `text` (e.g., `'00:01:00'`)

### Comments (PostgreSQL only)
- Every table MUST have a `comment on table` explaining its purpose
- Every column MUST have a `comment on column` explaining its meaning, EXCEPT for these obvious/standard columns: `uid`, `created_at`, `updated_at`, `deleted_at`
- Comments go immediately after the `create table` statement and its indexes
- Use concise, direct language — describe what it IS, not what it does
- For enum-like columns, list all valid values in the comment
- For nullable columns, explain what NULL means

### SQLite differences
- SQLite does not support `comment on` — use SQL `--` inline comments on each column instead
- Add a `-- table_name: description` comment before each `create table`
- No regex CHECK constraints — use `length()` checks instead
- No partial unique indexes with `WHERE region IS NOT NULL` — use simpler composite indexes
- Use `(datetime('now'))` instead of `now()` for defaults

### Indexes
- Prefix index names with `idx_` for regular indexes
- Unique indexes use descriptive names (e.g., `checks_slug_idx`, `check_labels_check_label_idx`)
- Always add partial indexes with `where deleted_at is null` for soft-deletable tables
- Foreign key columns that are queried frequently should have an index

### Multi-tenancy
- Most tables are scoped to an organization via `organization_uid`
- System-wide tables (e.g., `users`, `workers`) don't require `organization_uid`

## When creating new migrations (during a release cycle)
1. Add scratch migration files (e.g., `002_add_foo.up.sql`) to **both** `server/internal/db/postgres/migrations/` and `server/internal/db/sqlite/migrations/`
2. PostgreSQL: full `comment on` statements; SQLite: `--` inline comments
3. Ensure both produce the same logical schema
4. Run `make test` to verify both backends work

## At release time (squash step)
1. Capture a golden schema from the current incremental migrations (fresh DB dump)
2. Author one `NNN_vX_Y_Z.up.sql` per engine containing the **net final DDL** (not concatenation — hand-write to drop columns/tables that were added then removed)
3. Delete all scratch migrations for this cycle
4. Run the new single migration on a fresh DB, dump schema, diff against golden — must match
5. Run `make test` to confirm all backends pass
