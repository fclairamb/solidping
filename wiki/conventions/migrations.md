# Migration conventions

Read when creating or modifying migration files, tables, columns or indexes.

## Migration file naming and consolidation rule

**One migration file per release.**

- File pattern: `NNN_v<major>_<minor>_<patch>.up.sql` / `.down.sql`  
  Example: `001_v0_1_0.up.sql`, `002_v0_2_0.up.sql`
- `NNN` is a 3-digit zero-padded sequence. Each new release increments it by 1 and re-syncs both engines to the same number.
- **No dots in filenames** — Bun's discovery regex (`migrate/migrations.go`) allows only `[0-9a-z_\-]`; dots silently truncate the name.
- **Freeze-released-migrations**: a migration file that is part of a `vX.Y.Z` git tag is **frozen and never edited**, not even a comment or the `.down.sql`. Add the next `NNN` file instead.
  - The name does not tell you whether it shipped: release-please picks the version. `027_v0_39_0` was meant for v0.39.0 and shipped in v0.38.1. Check with `git tag --contains $(git log --diff-filter=A --format=%h -1 -- <file>)`.
  - `scripts/check-frozen-migrations.sh` (`make lint-migrations`, and the backend lint job in CI) compares every migration with its copy in the first tag that carried it, from v0.38.0 on.
  - Why: v0.38.4 appended two sections to 027 after v0.38.1 had shipped it. The guard refused to boot every database upgraded from v0.38.1..v0.38.3. v0.38.5 put 027 back, moved the sections to 028, and `migrationguard.Superseded` marks 028 applied on databases that ran the v0.38.4 copy.
- **During a release cycle**: add incremental scratch migrations as usual (`002_add_foo.up.sql`, etc.). At release time, collapse them into a single `NNN_vX_Y_Z.up.sql` and delete the scratch files.
- The `.down.sql` drops all tables in reverse dependency order (parity only; downs are never run in production).

## Transactions and failure behaviour

- The migrator records a migration as applied only after it succeeded. One that fails or is
  killed half way stays pending and runs again at the next boot (spec 2026-10-13-01). Write the
  statements so a rerun is harmless where you can (`if not exists`), since none of the existing
  27 files runs in a transaction.
- **New files are named `NNN_vX_Y_Z.tx.up.sql`** (and `.tx.down.sql`) so bun runs the whole
  file in one transaction and a failure rolls back every statement. The `.tx` suffix does not
  change the recorded name (bun keys on the numeric prefix).
- Use the plain `.up.sql` name only when the file needs autocommit: SQLite `PRAGMA foreign_keys`
  (a no-op inside a transaction; see 005, 009, 010) and PostgreSQL `CREATE INDEX CONCURRENTLY`.
  Say why in the file header.
- Never rename the existing files: they are recorded on every install.
- SQLite builds a brand new database under `solidping.db.init` and renames it when fully
  migrated; an existing one is copied to `solidping.db.pre-<first pending>.bak` before pending
  migrations run (latest backup only).

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
