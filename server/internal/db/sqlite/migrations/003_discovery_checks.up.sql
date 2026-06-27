-- solidping — discovery rework: check-centric grouped model (spec 2026-06-21-00).
-- SQLite mirror of the Postgres migration (uuid→text, jsonb→text,
-- timestamptz→text, now()→datetime('now')). Clean break: discovered_hosts is
-- dropped (results are regenerable by re-scan).

drop table if exists discovered_hosts;

create table discovered_checks (
  uid                   text primary key,
  organization_uid      text not null references organizations(uid),
  job_uid               text not null,
  source                text not null,
  group_key             text not null,
  group_label           text not null,
  name                  text not null,
  slug                  text not null,
  type                  text not null,
  config                text not null default '{}',
  metadata              text,
  promoted_to_check_uid text references checks(uid),
  discovered_at         text not null default (datetime('now')),
  created_at            text not null default (datetime('now')),
  updated_at            text not null default (datetime('now')),
  deleted_at            text
);

-- One row per (group, slug) per source while active & unpromoted → upsert key.
create unique index idx_discovered_checks_identity_active
  on discovered_checks (organization_uid, source, group_key, slug)
  where deleted_at is null and promoted_to_check_uid is null;

-- List/group by scan, and source-filter.
create index idx_discovered_checks_job        on discovered_checks (job_uid)                  where deleted_at is null;
create index idx_discovered_checks_org_source on discovered_checks (organization_uid, source) where deleted_at is null;
