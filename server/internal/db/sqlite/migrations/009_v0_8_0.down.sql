-- Teardown/parity only — never run in production. Reverses 009_v0_8_0.up.sql.
-- Reverse order: later-appended feature blocks are torn down before the
-- earlier ones they were stacked on top of.

-- reverse platform-operated "system agents" (spec 2026-07-27-01)
-- System rows have no organization and cannot survive the NOT NULL restore, so
-- the rebuild simply drops them.
drop table if exists agent_nonces;

create table agent_enrollment_tokens_old (
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

insert into agent_enrollment_tokens_old (
  uid, organization_uid, region, token_hash, expires_at,
  used_at, used_by_agent_uid, created_by_user_uid, created_at, deleted_at
)
select
  uid, organization_uid, region, token_hash, expires_at,
  used_at, used_by_agent_uid, created_by_user_uid, created_at, deleted_at
from agent_enrollment_tokens where kind = 'org';

drop table agent_enrollment_tokens;
alter table agent_enrollment_tokens_old rename to agent_enrollment_tokens;

create unique index idx_agent_enrollment_tokens_hash on agent_enrollment_tokens (token_hash) where deleted_at is null;
create index idx_agent_enrollment_tokens_org on agent_enrollment_tokens (organization_uid) where deleted_at is null;

create table agents_old (
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

insert into agents_old (
  uid, organization_uid, region, name, ed25519_public_key, x25519_public_key,
  fingerprint, status, last_seen_at, enrolled_at, revoked_at, created_at, updated_at, deleted_at
)
select
  uid, organization_uid, region, name, ed25519_public_key, x25519_public_key,
  fingerprint, status, last_seen_at, enrolled_at, revoked_at, created_at, updated_at, deleted_at
from agents where kind = 'org';

drop table agents;
alter table agents_old rename to agents;

create index idx_agents_org_region on agents (organization_uid, region) where deleted_at is null;
create index idx_agents_status     on agents (status)                    where deleted_at is null;
create unique index idx_agents_ed25519 on agents (ed25519_public_key)    where deleted_at is null;

-- reverse in-server ACME/TLS asset storage (spec 2026-07-26-01)
drop table if exists tls_storage_locks;
drop table if exists tls_storage;
