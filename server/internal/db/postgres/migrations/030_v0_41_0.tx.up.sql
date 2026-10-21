-- v0.41.0 — Postgres migration.
--
-- Frozen once tagged, like every migration: a later change goes in 031.
--
--   SECTION: user-preferences   users.preferences

-- ==========================================================================
-- SECTION: user-preferences (spec 2026-10-21-01)
-- ==========================================================================
-- The language and timezone a user's emails are written in. JSON so a new
-- preference needs no migration. No CHECK on purpose: the supported languages
-- and the tz database are validated in Go (internal/i18n), so adding a fifth
-- language is not a two-engine migration.
alter table users add column if not exists preferences jsonb;

--bun:split

comment on column users.preferences is 'User preferences the server follows when writing to the user: {"locale": "en|fr|de|es", "timezone": "<IANA zone>"}, each key optional. NULL when nothing was ever set (emails then fall back to English and UTC).';
