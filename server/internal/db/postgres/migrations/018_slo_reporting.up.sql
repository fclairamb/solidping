-- SLA/SLO reporting (spec 2026-08-20-01): an SLO object with a calendar-month
-- error budget, and scheduled uptime report digests.
--
-- Numbering: scratch migration for the still-unreleased v0.17.0 cycle. 014_v0_17_0
-- is that cycle's consolidated file; 015/016/017 are its scratch siblings and 018
-- is simply the next free number. It folds into 014_v0_17_0 at release
-- consolidation, same as its siblings.
--
-- Nothing per-window is stored: attainment, budget and history are always
-- computed at read time off the permanent `month` rollups. The only durable
-- artifact is the emailed report.

-- Maintenance tagging on raw results. This is ingest-time and therefore NOT
-- retroactive: every pre-existing row is honestly `false`, and the SLO status
-- API reports excludedMaintenanceSeconds explicitly so a partially-covered
-- month is legible rather than silently wrong.
alter table results add column if not exists maintenance boolean not null default false;

--bun:split

-- Aggregated counters, carried up hour -> day -> month next to total_checks /
-- successful_checks. Both are SUBSETS of those two, so every existing consumer
-- (status pages, badges, the availability API) is unchanged: raw availability
-- keeps counting maintenance probes. Only the SLO read path subtracts them.
alter table results add column if not exists maintenance_checks integer;

--bun:split

alter table results add column if not exists maintenance_successful_checks integer;

--bun:split

comment on column results.maintenance is
  'Raw rows only: an active maintenance window covered the check when this probe was recorded. Set at ingest, never backfilled.';

--bun:split

comment on column results.maintenance_checks is
  'Aggregated rows only: how many of total_checks were recorded under maintenance. Subset of total_checks.';

--bun:split

comment on column results.maintenance_successful_checks is
  'Aggregated rows only: how many of successful_checks were recorded under maintenance. Subset of both successful_checks and maintenance_checks.';

--bun:split

create table if not exists slos (
  uid                 uuid primary key default gen_random_uuid(),
  organization_uid    uuid not null references organizations(uid),
  name                text not null,
  slug                text not null,
  check_uid           uuid references checks(uid) on delete cascade,
  check_group_uid     uuid references check_groups(uid) on delete cascade,
  target_pct          numeric(6,3) not null,
  timezone            text not null default 'UTC',
  exclude_maintenance boolean not null default true,
  enabled             boolean not null default true,
  created_at          timestamptz not null default now(),
  updated_at          timestamptz not null default now(),
  deleted_at          timestamptz,
  -- Exactly one scope. A "both" SLO would have two different denominators and
  -- no defensible answer; a "neither" SLO would silently mean the whole org,
  -- which is a different feature. The schema refuses both rather than leaving
  -- it to whichever code path happens to read the row.
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

comment on table slos is
  'Service-level objectives (spec 2026-08-20-01). Windows are calendar months in `timezone`; nothing per-window is stored.';

--bun:split

comment on column slos.exclude_maintenance is
  'Subtract probes tagged results.maintenance from the SLO denominator. Only affects this SLO — status pages and badges keep showing raw availability.';

--bun:split

-- Scheduled uptime report digests. Deliberately NOT a column on `slos`: a
-- digest is useful for checks that carry no formal objective at all.
create table if not exists report_schedules (
  uid                   uuid primary key default gen_random_uuid(),
  organization_uid      uuid not null references organizations(uid),
  name                  text not null,
  frequency             text not null default 'monthly'
                          check (frequency in ('weekly', 'monthly')),
  timezone              text not null default 'UTC',
  -- PII. Same handling bar as status_page_subscribers.email: never echoed into
  -- events, never logged, only ever read back to the org's own admins.
  recipients            jsonb,
  check_uids            jsonb,
  check_group_uids      jsonb,
  include_slos          boolean not null default true,
  enabled               boolean not null default true,
  -- Start of the last period this schedule was actually reported for, in UTC.
  -- This is the duplicate-run suppression key: a second job fire for the same
  -- closed period is a no-op, so multi-replica claiming needs no leader.
  last_period_start     timestamptz,
  last_run_at           timestamptz,
  created_at            timestamptz not null default now(),
  updated_at            timestamptz not null default now(),
  deleted_at            timestamptz
);

--bun:split

create index if not exists idx_report_schedules_org_enabled
  on report_schedules (organization_uid, enabled)
  where deleted_at is null;

--bun:split

comment on table report_schedules is
  'Scheduled uptime report digests (spec 2026-08-20-01). Empty check_uids AND check_group_uids means org-wide.';

--bun:split

comment on column report_schedules.recipients is
  'JSON array of email addresses. PII — never log, never emit into events.';

--bun:split

comment on column report_schedules.last_period_start is
  'UTC start of the last reported period. Duplicate-run suppression key for JobTypeUptimeReport.';
