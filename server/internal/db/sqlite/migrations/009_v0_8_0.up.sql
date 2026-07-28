-- solidping v0.8.0 — see the postgres twin for the full rationale.
--
-- Multiple features share this one release migration; each contributes a
-- clearly separated block below. Append new blocks at the end.

-- ---------------------------------------------------------------------------
-- In-server ACME/TLS asset storage (spec 2026-07-26-01)
-- ---------------------------------------------------------------------------
-- certmagic key-value store for TLS assets. Values are opaque bytes (blob);
-- timestamps are text, the cross-database convention used across these
-- migrations. Prefix scans (List/Delete) use LIKE 'prefix%', which SQLite can
-- serve from the primary-key index directly.
--
-- SECURITY: this table contains PRIVATE KEYS. Never expose it through any API,
-- export, or debug surface — only internal/tlsedge reads it.

create table tls_storage (
  key text primary key, -- certmagic storage key: slash-separated path, no leading/trailing slash.
  value blob not null, -- Opaque asset bytes as written by certmagic (PEM, JSON, DER, ...).
  modified_at text not null -- Last write time; surfaced as certmagic KeyInfo.Modified.
);

-- Expiring distributed locks backing certmagic's Locker. A stale row
-- (expires_at in the past) may be taken over by any node.
create table tls_storage_locks (
  key text primary key,
  owner text not null, -- Opaque per-process identity; Unlock only releases a lock this process still owns.
  expires_at text not null
);

create index tls_storage_locks_expires_idx on tls_storage_locks (expires_at);

-- ---------------------------------------------------------------------------
-- Platform-operated "system agents" for cloud regions (spec 2026-07-27-01)
-- ---------------------------------------------------------------------------
-- See the postgres twin for the full rationale. SQLite has no ALTER COLUMN, so
-- making organization_uid nullable (it is NULL exactly for kind = 'system')
-- means rebuilding both agent tables with the established *_new pattern.

create table agents_new (
  uid                 text primary key,
  organization_uid    text,          -- NULL exactly when kind = 'system'
  kind                text not null default 'org',
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
  deleted_at          text,
  check (kind in ('org', 'system')),
  check ((kind = 'org') = (organization_uid is not null))
);

insert into agents_new (
  uid, organization_uid, kind, region, name, ed25519_public_key, x25519_public_key,
  fingerprint, status, last_seen_at, enrolled_at, revoked_at, created_at, updated_at, deleted_at
)
select
  uid, organization_uid, 'org', region, name, ed25519_public_key, x25519_public_key,
  fingerprint, status, last_seen_at, enrolled_at, revoked_at, created_at, updated_at, deleted_at
from agents;

drop table agents;
alter table agents_new rename to agents;

create index idx_agents_org_region on agents (organization_uid, region) where deleted_at is null;
create index idx_agents_status     on agents (status)                    where deleted_at is null;
create unique index idx_agents_ed25519 on agents (ed25519_public_key)    where deleted_at is null;
create index idx_agents_kind_region on agents (kind, region)             where deleted_at is null;

-- System enrollment tokens are multi-use (max_uses NULL = unlimited) and
-- revocable; org tokens stay strictly one-shot and never set max_uses.
create table agent_enrollment_tokens_new (
  uid                  text primary key,
  organization_uid     text,          -- NULL exactly when kind = 'system'
  kind                 text not null default 'org',
  region               text not null,
  token_hash           text not null,
  expires_at           text not null,
  max_uses             integer,
  use_count            integer not null default 0,
  used_at              text,
  used_by_agent_uid    text,
  created_by_user_uid  text,
  created_at           text not null default (datetime('now')),
  deleted_at           text,
  check (kind in ('org', 'system')),
  check ((kind = 'org') = (organization_uid is not null)),
  check (kind = 'system' or max_uses is null)
);

insert into agent_enrollment_tokens_new (
  uid, organization_uid, kind, region, token_hash, expires_at, use_count,
  used_at, used_by_agent_uid, created_by_user_uid, created_at, deleted_at
)
select
  uid, organization_uid, 'org', region, token_hash, expires_at,
  case when used_at is null then 0 else 1 end,
  used_at, used_by_agent_uid, created_by_user_uid, created_at, deleted_at
from agent_enrollment_tokens;

drop table agent_enrollment_tokens;
alter table agent_enrollment_tokens_new rename to agent_enrollment_tokens;

create unique index idx_agent_enrollment_tokens_hash on agent_enrollment_tokens (token_hash) where deleted_at is null;
create index idx_agent_enrollment_tokens_org on agent_enrollment_tokens (organization_uid) where deleted_at is null;

-- Cluster-wide replay guard for agent reconnect signatures (the per-process
-- nonce cache is only sound with a single API replica).
create table agent_nonces (
  agent_uid text not null,
  nonce     text not null,
  seen_at   text not null,
  primary key (agent_uid, nonce)
);

create index agent_nonces_seen_at_idx on agent_nonces (seen_at);

-- ---------------------------------------------------------------------------
-- (append further v0.8.0 blocks below this line)
-- ---------------------------------------------------------------------------
