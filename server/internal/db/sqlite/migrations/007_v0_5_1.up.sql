-- solidping v0.5.1 — SQLite mirror of the Postgres 007_v0_5_1 backfill patch
-- (see the Postgres file for the full story: an early draft of
-- 006_v0_5_0.up.sql got run against k8xp's Postgres solidping_dev database
-- mid-cycle, and bun's name-by-numeric-prefix tracking then silently skipped
-- the fuller migration that later shipped under the same "006" prefix).
--
-- SQLite installs are not known to have hit this drift — `make migrate` /
-- devloop always apply the final, already-consolidated 006_v0_5_0.up.sql in
-- one shot, never a partial mid-cycle draft. This file only guards the
-- agents/agent_enrollment_tokens tables (CREATE TABLE/INDEX IF NOT EXISTS is
-- safe and keeps directory/numbering parity with the Postgres migration). It
-- deliberately skips the checks/check_jobs/organizations column adds:
-- SQLite's ALTER TABLE has no ADD COLUMN IF NOT EXISTS, and those columns
-- already exist on every SQLite database that ran 006_v0_5_0 in full, so an
-- unguarded ADD COLUMN here would fail with "duplicate column name" on every
-- healthy install.

create table if not exists agents (
  uid                 text primary key,
  organization_uid    text not null,
  region              text not null,
  name                text not null,
  ed25519_public_key  text not null,
  x25519_public_key   text not null,
  fingerprint         text not null,
  status              text not null default 'active',
  last_seen_at        text,
  enrolled_at         text not null default (datetime('now')),
  revoked_at          text,
  created_at          text not null default (datetime('now')),
  updated_at          text not null default (datetime('now')),
  deleted_at          text
);

create index if not exists idx_agents_org_region on agents (organization_uid, region) where deleted_at is null;
create index if not exists idx_agents_status     on agents (status)                    where deleted_at is null;
create unique index if not exists idx_agents_ed25519 on agents (ed25519_public_key)    where deleted_at is null;

create table if not exists agent_enrollment_tokens (
  uid                  text primary key,
  organization_uid     text not null,
  region               text not null,
  token_hash           text not null,
  expires_at           text not null,
  used_at              text,
  used_by_agent_uid    text,
  created_by_user_uid  text,
  created_at           text not null default (datetime('now')),
  deleted_at           text
);

create unique index if not exists idx_agent_enrollment_tokens_hash on agent_enrollment_tokens (token_hash) where deleted_at is null;
create index if not exists idx_agent_enrollment_tokens_org on agent_enrollment_tokens (organization_uid) where deleted_at is null;
