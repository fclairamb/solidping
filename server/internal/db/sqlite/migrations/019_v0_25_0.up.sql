-- v0.25.0 — SQLite mirror of the ONE consolidated migration for the (still
-- unreleased) v0.25.0 release. See postgres/migrations/019_v0_25_0.up.sql for
-- the full rationale of each section.
--
--   SECTION: demo-account   users.demo + checks.created_by

-- ==========================================================================
-- SECTION: demo-account
--
-- SQLite has no `add column if not exists`, so these are plain ADD COLUMNs.
-- Both are nullable-or-defaulted, which is the only shape SQLite's ALTER TABLE
-- accepts on a populated table.
-- ==========================================================================

alter table users add column demo boolean not null default 0;

--bun:split

alter table checks add column created_by varchar(36);
