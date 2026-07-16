-- solidping v0.6.0 — deported (org-scoped) check agents over WebSocket
-- (spec 2026-07-16-02). SQLite mirror of the Postgres migration. Adds the
-- agents + agent_enrollment_tokens tables and removes the legacy plaintext
-- workers.token column (replaced by WebSocket agent transport with Ed25519
-- signature auth).

create table agents (
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

create index idx_agents_org_region on agents (organization_uid, region) where deleted_at is null;
create index idx_agents_status     on agents (status)                    where deleted_at is null;
create unique index idx_agents_ed25519 on agents (ed25519_public_key)    where deleted_at is null;

create table agent_enrollment_tokens (
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

create unique index idx_agent_enrollment_tokens_hash on agent_enrollment_tokens (token_hash) where deleted_at is null;
create index idx_agent_enrollment_tokens_org on agent_enrollment_tokens (organization_uid) where deleted_at is null;

-- Drop the legacy edge-worker bearer token (see the Postgres mirror). SQLite
-- refuses to drop an indexed column, so its index goes first.
drop index if exists idx_workers_token;
alter table workers drop column token;
