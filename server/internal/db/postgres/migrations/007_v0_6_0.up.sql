-- solidping v0.6.0 — deported (org-scoped) check agents over WebSocket
-- (spec 2026-07-16-02). Adds the agents + agent_enrollment_tokens tables and
-- removes the legacy plaintext workers.token column (the HTTP edge-worker API
-- and its spw_ bearer tokens are replaced by the WebSocket agent transport,
-- which authenticates by Ed25519 signature and never stores a usable
-- credential — only public keys).

-- Deported agents: one row per enrolled agent, hard-scoped to exactly one
-- private region (`@<org>/<region>`). The DB holds only public keys.
create table agents (
  uid                 uuid        primary key default gen_random_uuid(),
  organization_uid    uuid        not null references organizations(uid),
  region              text        not null,
  name                text        not null,
  ed25519_public_key  text        not null,
  x25519_public_key   text        not null,
  fingerprint         text        not null,
  status              text        not null default 'active',
  last_seen_at        timestamptz,
  enrolled_at         timestamptz not null default now(),
  revoked_at          timestamptz,
  created_at          timestamptz not null default now(),
  updated_at          timestamptz not null default now(),
  deleted_at          timestamptz
);

create index idx_agents_org_region on agents (organization_uid, region) where deleted_at is null;
create index idx_agents_status     on agents (status)                    where deleted_at is null;
create unique index idx_agents_ed25519 on agents (ed25519_public_key)    where deleted_at is null;

comment on table agents is 'Deported (org-scoped) check agents connecting outbound over WebSocket. Hard-scoped to one private region; DB stores only public keys.';
comment on column agents.region is 'Fully-qualified private-region slug (@<org>/<region>) the agent is bound to.';
comment on column agents.ed25519_public_key is 'Base64 Ed25519 identity public key; verifies reconnect signatures.';
comment on column agents.x25519_public_key is 'age X25519 recipient (age1...) credentials are sealed to.';

-- One-shot enrollment tokens: bind a future agent to (org, region). Only the
-- SHA-256 hash of the spe_ token is stored; single-use under concurrency.
create table agent_enrollment_tokens (
  uid                  uuid        primary key default gen_random_uuid(),
  organization_uid     uuid        not null references organizations(uid),
  region               text        not null,
  token_hash           text        not null,
  expires_at           timestamptz not null,
  used_at              timestamptz,
  used_by_agent_uid    uuid,
  created_by_user_uid  uuid,
  created_at           timestamptz not null default now(),
  deleted_at           timestamptz
);

create unique index idx_agent_enrollment_tokens_hash on agent_enrollment_tokens (token_hash) where deleted_at is null;
create index idx_agent_enrollment_tokens_org on agent_enrollment_tokens (organization_uid) where deleted_at is null;

comment on table agent_enrollment_tokens is 'One-shot agent enrollment tokens (spe_ prefix). Stores only the SHA-256 hash; consumed atomically at enrollment.';

-- Drop the legacy edge-worker bearer token. The HTTP worker API that minted and
-- matched these plaintext spw_ tokens is gone; agents authenticate by Ed25519
-- signature, so the DB holds no usable worker/agent credential at all. The
-- partial index on the column drops with it.
alter table workers drop column token;
