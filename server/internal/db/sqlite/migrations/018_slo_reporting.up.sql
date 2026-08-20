-- SQLite mirror of postgres/migrations/018_slo_reporting.up.sql — SLA/SLO
-- reporting (spec 2026-08-20-01). See the Postgres file for the full rationale,
-- in particular why the maintenance counters are subsets of the existing ones
-- and therefore change no existing consumer.
--
-- Numbering: scratch migration for the still-unreleased v0.17.0 cycle; folds
-- into 014_v0_17_0 at release consolidation.
--
-- SQLite has no ADD COLUMN IF NOT EXISTS, so this file is not re-runnable —
-- same as every other scratch migration this cycle.

alter table results add column maintenance integer not null default 0;

--bun:split

alter table results add column maintenance_checks integer;

--bun:split

alter table results add column maintenance_successful_checks integer;

--bun:split

create table if not exists slos (
  uid                 text primary key,
  organization_uid    text not null references organizations(uid),
  name                text not null,
  slug                text not null,
  check_uid           text references checks(uid) on delete cascade,
  check_group_uid     text references check_groups(uid) on delete cascade,
  target_pct          real not null,
  timezone            text not null default 'UTC',
  exclude_maintenance integer not null default 1,
  enabled             integer not null default 1,
  created_at          text not null default (datetime('now')),
  updated_at          text not null default (datetime('now')),
  deleted_at          text,
  -- Exactly one scope; see the Postgres file.
  constraint slos_scope_xor check (
    (check_uid is not null and check_group_uid is null)
    or (check_uid is null and check_group_uid is not null)
  ),
  constraint slos_target_pct_range check (target_pct > 0 and target_pct <= 100)
);

--bun:split

create unique index if not exists uq_slos_org_slug
  on slos (organization_uid, slug)
  where deleted_at is null;

--bun:split

create index if not exists idx_slos_org_enabled
  on slos (organization_uid, enabled)
  where deleted_at is null;

--bun:split

create index if not exists idx_slos_check
  on slos (check_uid)
  where check_uid is not null and deleted_at is null;

--bun:split

create index if not exists idx_slos_check_group
  on slos (check_group_uid)
  where check_group_uid is not null and deleted_at is null;

--bun:split

-- recipients / check_uids / check_group_uids are JSON arrays stored as text.
-- recipients is PII — never log it, never emit it into events.
create table if not exists report_schedules (
  uid               text primary key,
  organization_uid  text not null references organizations(uid),
  name              text not null,
  frequency         text not null default 'monthly'
                      check (frequency in ('weekly', 'monthly')),
  timezone          text not null default 'UTC',
  recipients        text,
  check_uids        text,
  check_group_uids  text,
  include_slos      integer not null default 1,
  enabled           integer not null default 1,
  last_period_start text,
  last_run_at       text,
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text
);

--bun:split

create index if not exists idx_report_schedules_org_enabled
  on report_schedules (organization_uid, enabled)
  where deleted_at is null;
