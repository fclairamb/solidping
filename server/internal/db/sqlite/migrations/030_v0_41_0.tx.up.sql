-- v0.41.0 — SQLite twin of the Postgres 030_v0_41_0 migration.
--
-- Frozen once tagged, like every migration: a later change goes in 031.

-- SECTION: user-preferences (spec 2026-10-21-01)
-- users.preferences: the language and timezone a user's emails are written in,
-- as JSON {"locale": "en|fr|de|es", "timezone": "<IANA zone>"}; NULL = never
-- set (English, UTC). Validated in Go, no CHECK.
alter table users add column preferences text;
