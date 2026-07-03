-- solidping v0.2.0 — rollback for the consolidated delta over v0.1.0 (SQLite,
-- reverse order of 002_v0_2_0.up.sql). Restores exactly the v0.1.0 baseline
-- shape. Used only for full schema teardown; never run in production.

drop index if exists idx_check_jobs_claim_fast;
drop index if exists idx_check_jobs_claim_slow;

alter table check_jobs drop column lane;
alter table check_jobs drop column delay_ewma_ms;
alter table check_jobs drop column effective_scheduled_at;
alter table check_jobs drop column plan_weight;
alter table check_jobs drop column cost_ewma_ms;

alter table checks drop column last_outage_at;
alter table checks drop column flap_count;
alter table checks drop column max_recovery_multiplier;
alter table checks drop column flap_backoff_factor;
alter table checks drop column flapping_window_seconds;

alter table checks add column max_adaptive_increase integer; -- Maximum multiplier for adaptive resolution increase. NULL uses system default

alter table status_pages drop column history_period;

drop table if exists discovered_checks;

create table discovered_hosts (
  uid                   text primary key,
  organization_uid      text not null references organizations(uid),
  job_uid               text not null references jobs(uid),
  ip                    text not null,
  hostname              text,
  open_ports            text not null default '[]',
  icmp_reachable        integer not null default 0,
  suggested_checks      text not null default '[]',
  promoted_to_check_uid text references checks(uid),
  discovered_at         text not null default (datetime('now')),
  deleted_at            text,
  source                text not null default 'lan'
);

create unique index idx_discovered_hosts_org_ip_source_active on discovered_hosts (organization_uid, ip, source)
  where deleted_at is null and promoted_to_check_uid is null;
create index idx_discovered_hosts_org_job on discovered_hosts (organization_uid, job_uid) where deleted_at is null;

-- Rebuild user_tokens with the v0.1.0 type vocabulary (SQLite cannot alter
-- check constraints). OAuth refresh grants are dropped by the copy filter;
-- their loss is fine: downs are parity-only.
create table user_tokens_old (
  uid               text primary key,
  user_uid          text not null references users(uid) on delete cascade, -- Token owner
  organization_uid  text references organizations(uid) on delete cascade, -- Organization scope for PAT tokens. NULL for global refresh tokens
  token             text not null, -- Hashed token value
  type              text not null check (type in ('pat', 'refresh')), -- Token type: pat or refresh
  properties        text, -- Token metadata (e.g., name, scopes, IP restrictions)
  expires_at        text, -- Expiration timestamp. NULL means never expires
  last_active_at    text, -- Last time this token was used for authentication
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text
);

insert into user_tokens_old (
  uid, user_uid, organization_uid, token, type, properties,
  expires_at, last_active_at, created_at, updated_at, deleted_at
)
select
  uid, user_uid, organization_uid, token, type, properties,
  expires_at, last_active_at, created_at, updated_at, deleted_at
from user_tokens
where type in ('pat', 'refresh');

drop table user_tokens;
alter table user_tokens_old rename to user_tokens;

create unique index user_tokens_token_idx on user_tokens (token) where deleted_at is null;
create index user_tokens_user_uid_idx on user_tokens (user_uid) where deleted_at is null;
create index user_tokens_expires_at_idx on user_tokens (expires_at) where deleted_at is null and expires_at is not null;

drop table if exists oauth_clients;
