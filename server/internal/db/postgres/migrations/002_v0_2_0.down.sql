-- solidping v0.2.0 — rollback for the consolidated delta over v0.1.0 (reverse
-- order of 002_v0_2_0.up.sql). Restores exactly the v0.1.0 baseline shape.
-- Used only for full schema teardown; never run in production.

drop index if exists idx_check_jobs_claim_fast;
drop index if exists idx_check_jobs_claim_slow;

alter table check_jobs drop column if exists lane;
alter table check_jobs drop column if exists delay_ewma_ms;
alter table check_jobs drop column if exists effective_scheduled_at;
alter table check_jobs drop column if exists plan_weight;
alter table check_jobs drop column if exists cost_ewma_ms;

alter table checks drop column if exists last_outage_at;
alter table checks drop column if exists flap_count;
alter table checks drop column if exists max_recovery_multiplier;
alter table checks drop column if exists flap_backoff_factor;
alter table checks drop column if exists flapping_window_seconds;

alter table checks add column max_adaptive_increase integer;

comment on column checks.max_adaptive_increase is 'Maximum multiplier for adaptive resolution increase. NULL uses system default.';

alter table status_pages drop column if exists history_period;

drop table if exists discovered_checks;

create table discovered_hosts (
  uid                   uuid primary key,
  organization_uid      uuid not null references organizations(uid),
  job_uid               uuid not null references jobs(uid),
  ip                    inet not null,
  hostname              text,
  open_ports            jsonb not null default '[]',
  icmp_reachable        boolean not null default false,
  suggested_checks      jsonb not null default '[]',
  promoted_to_check_uid uuid references checks(uid),
  discovered_at         timestamptz not null default now(),
  deleted_at            timestamptz,
  source                text not null default 'lan'
);

create unique index idx_discovered_hosts_org_ip_source_active on discovered_hosts (organization_uid, ip, source)
  where deleted_at is null and promoted_to_check_uid is null;
create index idx_discovered_hosts_org_job on discovered_hosts (organization_uid, job_uid) where deleted_at is null;

drop table if exists oauth_refresh_tokens;
drop table if exists oauth_auth_codes;
drop table if exists oauth_clients;
