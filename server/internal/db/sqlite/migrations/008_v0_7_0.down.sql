-- Teardown/parity only — never run in production. Reverses 008_v0_7_0.up.sql.
-- Reverse order: later-appended feature blocks are torn down before the
-- earlier ones they were stacked on top of.

-- reverse status page group resources (spec 2026-08-01-03)
-- Rebuild back to the check-only shape; group-targeting rows cannot survive the
-- NOT NULL restore on check_uid and are dropped.
create table status_page_resources_old (
  uid             text primary key,
  section_uid     text not null references status_page_sections(uid) on delete cascade,
  check_uid       text not null references checks(uid) on delete cascade,
  public_name     text,
  explanation     text,
  position        integer not null default 0,
  created_at      text not null default (datetime('now')),
  updated_at      text not null default (datetime('now'))
);

insert into status_page_resources_old (
  uid, section_uid, check_uid, public_name, explanation, position, created_at, updated_at
)
select
  uid, section_uid, check_uid, public_name, explanation, position, created_at, updated_at
from status_page_resources
where check_uid is not null;

drop table status_page_resources;
alter table status_page_resources_old rename to status_page_resources;

create unique index status_page_resources_section_check_idx on status_page_resources (section_uid, check_uid);
create index status_page_resources_check_idx on status_page_resources (check_uid);

-- reverse per-status-page custom CSS (spec 2026-07-27-02)
alter table status_pages drop column custom_css;

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

-- reverse results table tier-1 storage trim (spec 2026-07-24-02)
-- No backfill: availability_pct is recomputable from successful_checks /
-- total_checks, and last_for_status is repopulated by the next result insert.
alter table results add column last_for_status integer;
alter table results add column availability_pct real;

create index idx_results_last_for_status on results (check_uid, status) where last_for_status = 1;

-- reverse monthly SMS/voice usage counters (spec 2026-07-22-02)
drop table if exists org_usage_counters;

-- reverse phone contact verification (spec 2026-07-22-02)
alter table user_contacts drop column verify_attempts;
alter table user_contacts drop column verify_expires_at;
alter table user_contacts drop column verify_code_hash;

-- reverse custom domains for status pages (spec 2026-07-22-01)
drop index if exists status_pages_custom_domain_idx;
alter table status_pages drop column custom_domain_failures;
alter table status_pages drop column custom_domain_checked_at;
alter table status_pages drop column custom_domain_verified_at;
alter table status_pages drop column custom_domain_token;
alter table status_pages drop column custom_domain;

-- ---------------------------------------------------------------------------
-- (append further v0.7.0 teardown blocks below this line)
-- ---------------------------------------------------------------------------
