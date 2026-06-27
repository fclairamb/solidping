-- solidping — discovery rework: check-centric grouped model (spec 2026-06-21-00).
-- Discovery is no longer host-centric. The stored unit is a *suggested check*;
-- rows are grouped for display by group_key (an IP, container ID, workload uid…).
-- Clean break: discovered_hosts is dropped (results are regenerable by re-scan).

drop table if exists discovered_hosts;

create table discovered_checks (
  uid                   uuid        primary key default gen_random_uuid(),
  organization_uid      uuid        not null references organizations(uid),
  job_uid               uuid        not null,
  source                text        not null,
  group_key             text        not null,
  group_label           text        not null,
  name                  text        not null,
  slug                  text        not null,
  type                  text        not null,
  config                jsonb       not null default '{}',
  metadata              jsonb,
  promoted_to_check_uid uuid        references checks(uid),
  discovered_at         timestamptz not null default now(),
  created_at            timestamptz not null default now(),
  updated_at            timestamptz not null default now(),
  deleted_at            timestamptz
);

-- One row per (group, slug) per source while active & unpromoted → upsert key.
create unique index idx_discovered_checks_identity_active
  on discovered_checks (organization_uid, source, group_key, slug)
  where deleted_at is null and promoted_to_check_uid is null;

-- List/group by scan, and source-filter.
create index idx_discovered_checks_job        on discovered_checks (job_uid)                  where deleted_at is null;
create index idx_discovered_checks_org_source on discovered_checks (organization_uid, source) where deleted_at is null;

comment on table discovered_checks is 'Suggested checks produced by discovery scans, grouped for display by group_key. Ephemeral scratch data — regenerable by re-scanning.';
comment on column discovered_checks.group_key is 'Stable grouping identity (IP, container ID, workload uid). A render-time GROUP BY, not a second table.';
comment on column discovered_checks.metadata is 'Denormalized group-display hints (identical across a group''s rows), written by the suggester.';
