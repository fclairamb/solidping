-- v0.18.0 — SQLite mirror of the ONE consolidated migration for the (still
-- unreleased) v0.18.0 release. 014_v0_17_0 is the last RELEASED migration (tag
-- v0.17.0), so everything this cycle produces lands here, in a single file per
-- dialect, per the repo convention documented in wiki/conventions/database.md.
--
-- It is organised into SECTIONs mirroring postgres/migrations/015_v0_18_0.up.sql
-- one for one, each one preserving that section's own rationale:
--
--   SECTION: generic-attachments        files.topic/details attachment link
--
-- ORDER IS LOAD-BEARING. Sections run top to bottom and later ones build on
-- earlier ones. The .down.sql unwinds them in the exact reverse order.
--
-- The SECTION banners are also a machine-readable anchor: migration tests slice
-- one section out of this file so they can replay just that block against a
-- populated database. Renaming a section renames a test fixture — see
-- migrationSection() in the db test packages.

-- ==========================================================================
-- SECTION: generic-attachments
-- Was scratch migration 020_generic_attachments (spec 2026-08-21-01). Written
-- against 014_v0_17_0 on the stale premise that v0.17.0 was unreleased; moved
-- here verbatim once the tag proved it had shipped — a released migration is
-- never modified, because a database that already ran it never re-runs it.
-- ==========================================================================

-- SQLite mirror of the generic-attachments section of
-- postgres/migrations/015_v0_18_0.up.sql — a `files` row can now say what it
-- is attached to (spec 2026-08-21-01). See the Postgres file for the full
-- rationale: why the key is path-like, why NULL is the norm, why the index is
-- partial, and why an attachment must never reach a public surface.
--
-- Dialect differences, both cosmetic:
--   * `details` is TEXT here, like every other jsonb column in the SQLite
--     schema — models.JSONMap marshals to a string either way.
--   * SQLite has no `ADD COLUMN IF NOT EXISTS`, so these two statements are
--     not re-runnable — same as every other section in the SQLite set.
alter table files add column topic text;

--bun:split

alter table files add column details text;

--bun:split

create index if not exists files_org_topic_idx
  on files (organization_uid, topic)
  where deleted_at is null and topic is not null;
