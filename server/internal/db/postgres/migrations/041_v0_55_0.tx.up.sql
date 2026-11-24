-- v0.55.0 — postgres migration.
--
--   SECTION: org-event-feeds   org_event_feeds
--
-- ==========================================================================
-- SECTION: org-event-feeds
-- ==========================================================================
-- Private Atom feeds of an organization's incidents and events (spec
-- 2026-11-24-12, #1014). The secret in the feed URL is the only credential, so
-- only its SHA-256 (hex) is stored: a database read never yields a working
-- URL. Several named feeds per org (capped at 10 by the service), each revoked
-- on its own (deleted_at). event_types is the feed's scope as a JSON array of
-- exact types ("check.created") and families ("incident.*").
create table if not exists org_event_feeds (
  uid                 uuid        primary key,
  organization_uid    uuid        not null references organizations(uid) on delete cascade,
  name                text        not null,
  secret_hash         text        not null,
  event_types         jsonb       not null default '[]'::jsonb,
  created_by_user_uid uuid        references users(uid) on delete set null,
  created_at          timestamptz not null default now(),
  rotated_at          timestamptz,
  last_used_at        timestamptz,
  deleted_at          timestamptz
);

--bun:split

create unique index if not exists idx_org_event_feeds_secret_hash on org_event_feeds (secret_hash);

--bun:split

create index if not exists idx_org_event_feeds_org on org_event_feeds (organization_uid) where deleted_at is null;
