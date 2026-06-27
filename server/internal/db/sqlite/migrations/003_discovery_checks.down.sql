-- Rollback for the discovery check-centric model (SQLite). Drops
-- discovered_checks and recreates discovered_hosts per its original 001
-- definition (empty — rows are regenerable by re-scanning).

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
