---
name: database
description: Database conventions for SolidPing SQL migrations. Use when creating or modifying migration files, tables, columns, or indexes.
---

# Database Conventions

## Migration file naming
- Use sequential numbering: `001_name.up.sql`, `001_name.down.sql`
- Keep a single consolidated migration per major version; avoid incremental ALTER TABLE migrations

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

## When creating new migrations
1. Add the PostgreSQL migration with full `comment on` statements
2. Add the equivalent SQLite migration with `--` comments
3. Ensure both produce the same logical schema
4. Run `make test` to verify both backends work
