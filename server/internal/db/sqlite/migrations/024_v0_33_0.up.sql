-- v0.33.0 — the ONE consolidated SQLite migration for the (still unreleased)
-- v0.33.0 release. 023_v0_32_0 shipped, so this is the next free number and
-- every schema change of this cycle is appended here as a new SECTION, per
-- wiki/conventions/database.md. Mirrors the Postgres twin (024_v0_33_0) section
-- for section.
--
--   SECTION: check-freshness   checks.last_result_at, its backfill and the
--                              freshness-sweep index
--   SECTION: passive-checks-no-regions
--                              heartbeat/email checks lose their regions and
--                              keep one NULL-region job
--   SECTION: auto-region-placement
--                              checks.placement / region_count / region_pool,
--                              and checks on the system default regions
--                              switch to automatic placement
--   SECTION: degraded-incident-kind
--                              Postgres only: incidents_kind_check accepts
--                              'degraded' (nothing to do here)
--   SECTION: drop-degraded-dry-run
--                              checks.degraded_would_fire_at goes, the degraded
--                              sweep reads only degraded_enabled checks
--   SECTION: multi-region-quorum
--                              checks.fail_quorum and the per-(check, region)
--                              reading table check_region_states
--
-- ⚠️ A DEV DATABASE THAT ALREADY RAN AN EARLIER DRAFT OF THIS FILE MUST BE
-- RESET, NEVER REPAIRED. bun keys an applied migration on its numeric prefix
-- alone, so appending a section to 024 does not re-run it: set
-- `SP_DB_RESET=true` (test/demo run mode) or delete the SQLite file.
-- **Do not run `solidping migrate repair`** — it rewrites the recorded
-- checksum without applying anything.

-- ==========================================================================
-- SECTION: check-freshness  (spec 2026-09-25-02)
--
-- See the Postgres twin for the rationale: the newest REAL result time,
-- denormalized so the freshness sweep that moves silent checks to `stale` (10)
-- is one indexed query. Backfilled from the newest real raw row; NULL when
-- there is none (the sweep then measures from created_at).
--
-- SQLite has no `add column if not exists`; this runs once on a database that
-- has never seen 024.
-- ==========================================================================

alter table checks add column last_result_at text;

--bun:split

update checks
   set last_result_at = (
     select max(r.period_start)
       from results r
      where r.organization_uid = checks.organization_uid
        and r.check_uid = checks.uid
        and r.period_type = 'raw'
        and r.status in (3, 4, 5, 6, 8)
   )
 where deleted_at is null;

--bun:split

create index if not exists idx_checks_freshness
  on checks (coalesce(last_result_at, created_at))
  where deleted_at is null and enabled = 1 and internal = 0 and status <> 10;

--bun:split

-- ==========================================================================
-- SECTION: passive-checks-no-regions  (spec 2026-09-25-04)
--
-- See the Postgres twin for the rationale: passive checks (heartbeat, email)
-- are evaluated on the jobs node from ONE NULL-region job. The type list
-- mirrors checkerdef.PassiveCheckTypes(). The surviving job is converted
-- rather than inserted, so its scheduled_at keeps the text encoding bun wrote.
-- ==========================================================================

update checks
   set regions = '[]'
 where type in ('heartbeat', 'email')
   and regions is not null
   and json_valid(regions)
   and json_array_length(regions) > 0;

--bun:split

delete from check_jobs
 where type in ('heartbeat', 'email')
   and region is not null
   and exists (
     select 1 from check_jobs n
      where n.check_uid = check_jobs.check_uid
        and n.region is null
   );

--bun:split

delete from check_jobs
 where type in ('heartbeat', 'email')
   and region is not null
   and uid <> (
     select min(k.uid) from check_jobs k
      where k.check_uid = check_jobs.check_uid
        and k.region is not null
   );

--bun:split

update check_jobs
   set region = null,
       lease_worker_uid = null,
       lease_expires_at = null,
       lease_starts = 0
 where type in ('heartbeat', 'email')
   and region is not null;

--bun:split

-- ==========================================================================
-- SECTION: auto-region-placement  (spec 2026-09-25-06)
--
-- See the Postgres twin for the rationale. region_pool is a JSON array in a
-- text column, like regions.
-- ==========================================================================

alter table checks add column placement text not null default 'pinned'
  check (placement in ('pinned', 'auto'));

--bun:split

alter table checks add column region_count integer;

--bun:split

alter table checks add column region_pool text;

--bun:split

-- A check whose regions are exactly the system default_regions (as a set)
-- becomes auto, region_count = its region count, empty pool. Regions and jobs
-- are untouched. Everything else stays pinned.
update checks
   set placement = 'auto',
       region_count = json_array_length(regions),
       region_pool = null
 where deleted_at is null
   and placement = 'pinned'
   and type not in ('heartbeat', 'email', 'private-location')
   and regions is not null
   and json_valid(regions)
   and json_array_length(regions) > 0
   and not exists (select 1 from json_each(checks.regions) r where r.value like '@%')
   and (select count(distinct r.value) from json_each(checks.regions) r) = json_array_length(regions)
   and exists (
     select 1 from parameters p
      where p.organization_uid is null
        and p.key = 'default_regions'
        and p.deleted_at is null
        and json_valid(p.value)
        and json_type(p.value, '$.value') = 'array'
        and (select count(distinct d.value) from json_each(p.value, '$.value') d)
          = (select count(distinct r.value) from json_each(checks.regions) r)
        and not exists (
          select 1 from json_each(checks.regions) r
           where r.value not in (select d.value from json_each(p.value, '$.value') d)
        )
   );

--bun:split

-- ==========================================================================
-- SECTION: degraded-incident-kind  (spec 2026-09-24-08)
--
-- Postgres only: its incidents_kind_check refused 'degraded'. SQLite never had
-- a constraint on incidents.kind, so there is nothing to do here.
-- ==========================================================================

-- ==========================================================================
-- SECTION: drop-degraded-dry-run  (spec 2026-09-24-08)
--
-- See the Postgres twin for the rationale: the degraded dry run (the
-- degraded_would_fire_at stamp, its banner and its list filter) is gone, a
-- disabled check is not evaluated at all, and 023 is released so the column is
-- dropped here.
-- ==========================================================================

alter table checks drop column degraded_would_fire_at;

--bun:split

drop index if exists idx_checks_degraded_eval;

--bun:split

create index if not exists idx_checks_degraded_eval
  on checks (degraded_evaluated_at)
  where deleted_at is null and enabled and degraded_enabled;

--bun:split

-- Close a degraded incident left open on a check whose flag is already off:
-- the new sweep never reads that check again.
update incidents
   set state = 2,
       resolved_at = datetime('now'),
       resolution_type = 'disabled',
       updated_at = datetime('now')
 where kind = 'degraded'
   and state = 1
   and deleted_at is null
   and exists (
     select 1 from checks c
      where c.uid = incidents.check_uid
        and c.degraded_enabled = 0
   );

--bun:split

-- ==========================================================================
-- SECTION: multi-region-quorum  (spec 2026-09-25-10)
--
-- See the Postgres twin for the rationale: checks.fail_quorum (NULL = the
-- default quorum) and the per-(check, region) newest reading the quorum
-- evaluates.
-- ==========================================================================

alter table checks add column fail_quorum text
  check (
    fail_quorum is null
    or fail_quorum in ('all', 'majority')
    or (
      length(fail_quorum) between 1 and 3
      and fail_quorum glob '[1-9]*'
      and fail_quorum not glob '*[^0-9]*'
      and cast(fail_quorum as integer) <= 100
    )
  );

--bun:split

create table if not exists check_region_states (
  check_uid         text not null references checks(uid) on delete cascade, -- The check
  region            text not null, -- Region slug the reading came from
  organization_uid  text not null references organizations(uid) on delete cascade, -- Owning organization
  status            integer not null, -- Result status of the newest real result (3 up, 4 down, 5 timeout, 6 error, 8 warning)
  status_since      text not null, -- When the region entered its current side (failing or passing)
  last_result_at    text not null, -- Execution time of the newest real result
  updated_at        text not null default (datetime('now')),
  primary key (check_uid, region)
);
