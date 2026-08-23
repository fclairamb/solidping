-- v0.18.0 (fourth file) — forced password rotation as a user-level capability
-- (spec 2026-08-23-04).
--
-- SQLite mirror of postgres/migrations/018_v0_18_0.up.sql. See the Postgres file
-- for why the flag is a general user-level capability rather than something
-- bolted onto the admin seed, and for why this is a FOURTH file rather than an
-- append to 015/016/017 (Bun keys applied migrations on the numeric prefix only).
--
--   SECTION: must-change-password   users.must_change_password
--
-- Dialect differences: boolean -> integer, no `comment on column`, and SQLite's
-- ALTER TABLE ADD COLUMN has no IF NOT EXISTS — the column is new in this
-- release, so a plain ADD is correct and any re-run would be a bug elsewhere.

-- ==========================================================================
-- SECTION: must-change-password
-- ==========================================================================

alter table users add column must_change_password integer not null default 0;
