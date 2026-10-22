-- v0.41.0 — SQLite twin of the Postgres 030_v0_41_0 migration.
--
-- Frozen once tagged, like every migration: a later change goes in 031.

-- SECTION: user-preferences (spec 2026-10-21-01)
-- users.preferences: the language and timezone a user's emails are written in,
-- as JSON {"locale": "en|fr|de|es", "timezone": "<IANA zone>"}; NULL = never
-- set (English, UTC). Validated in Go, no CHECK.
alter table users add column preferences text;

-- SECTION: discovered-checks-region (spec 2026-10-21-01)
-- discovered_checks.region: region of the scan that found the row; '' = the
-- server itself, '@slug' = a private region. NOT NULL (NULLs are distinct in
-- unique indexes) and part of the upsert identity.
alter table discovered_checks add column region text not null default '';

drop index if exists idx_discovered_checks_identity_active;

create unique index idx_discovered_checks_identity_active
  on discovered_checks (organization_uid, source, group_key, slug, region)
  where deleted_at is null and promoted_to_check_uid is null;
