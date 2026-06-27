-- Rollback for the discovery check-centric model. Drops discovered_checks and
-- recreates discovered_hosts per its original 001 definition (empty — rows are
-- regenerable by re-scanning).

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
