-- Teardown/parity half of the consolidated v0.18.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of 015_v0_18_0.up.sql,
-- so each one unwinds on a schema that still has everything the sections above
-- it created.
--
-- Several sections are lossy on the way down; each says so in its own note.

-- ==========================================================================
-- SECTION: status-page-password
-- Teardown half of the status-page-password section (spec 2026-08-21-07).
-- ==========================================================================

-- LOSSY, and more so than the Postgres half.
--
-- Dropping the hash makes every password page unopenable rather than public —
-- the API refuses to serve a `password` page it cannot check a password for.
-- That is the safe direction, but it takes those pages offline until an
-- operator re-sets a password or moves them back to `public`.
--
-- The widened CHECK constraint is NOT narrowed back: doing so would need a
-- second full table rebuild, and it would FAIL outright on any database still
-- holding a `password` row. Instead the rows are moved back to `private` first
-- (the safe reading: a page that was shared behind a secret must not become
-- world-readable because someone rolled a migration back), and the constraint
-- is left permissive. A permissive constraint on a database whose application
-- code predates the value is inert — models.ValidStatusPageVisibility is the
-- real gate.

update status_pages set visibility = 'private' where visibility = 'password';

--bun:split

alter table status_pages drop column password_hash;

-- ==========================================================================
-- SECTION: status-page-branding
-- Teardown half of the status-page-branding section (spec 2026-08-21-07).
-- ==========================================================================

-- LOSSY: dropping these columns forgets which blob was a page's logo/favicon
-- and every white-label opt-in. The `files` rows and their blobs survive, but
-- nothing points at them any more, so they stop being publicly reachable —
-- which is the safe direction to fail in.

drop index if exists status_pages_favicon_file_idx;

--bun:split

drop index if exists status_pages_logo_file_idx;

--bun:split

alter table status_pages drop column hide_branding;

--bun:split

alter table status_pages drop column favicon_file_uid;

--bun:split

alter table status_pages drop column logo_file_uid;

-- ==========================================================================
-- SECTION: generic-attachments
-- Was scratch migration 020_generic_attachments (spec 2026-08-21-01). Teardown half.
-- ==========================================================================

-- Teardown/parity only — never run in production. SQLite mirror of the
-- generic-attachments section of postgres/migrations/015_v0_18_0.down.sql.
--
-- LOSSY the same way the Postgres half is: dropping the columns destroys every
-- attachment link while leaving the blobs and their `files` rows behind.

drop index if exists files_org_topic_idx;

--bun:split

alter table files drop column details;

--bun:split

alter table files drop column topic;
