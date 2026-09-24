-- v0.33.0 — the ONE consolidated Postgres migration for the (still unreleased)
-- v0.33.0 release. 023_v0_32_0 shipped, so this is the next free number and
-- every schema change of this cycle is appended here as a new SECTION, per
-- wiki/conventions/database.md.
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
--
-- ⚠️ A DEV DATABASE THAT ALREADY RAN AN EARLIER DRAFT OF THIS FILE MUST BE
-- RESET, NEVER REPAIRED. bun keys an applied migration on its numeric prefix
-- alone, so appending a section to 024 does not re-run it: set
-- `SP_DB_RESET=true` (test/demo run mode) or drop and recreate the database.
-- **Do not run `solidping migrate repair`** — it rewrites the recorded
-- checksum without applying anything.

-- ==========================================================================
-- SECTION: check-freshness  (spec 2026-09-25-02)
--
-- checks.status only changed when a result arrived, so a check whose results
-- stopped kept its last status forever: on 2026-09-24 a region was dark for
-- 8 hours and its 12 pinned checks stayed `up`. The freshness sweep now moves
-- such a check to the `stale` status (10, "No data"). To keep that sweep one
-- indexed query instead of a scan of `results`, the newest REAL result time is
-- denormalized onto the check row. It is written by
-- incidents.ProcessCheckResult for every up/down/timeout/error/warning result,
-- including for checks inside a maintenance window.
--
-- Backfill: the newest real raw row per check (results_raw_idx serves the
-- correlated MAX). A check with no raw row inside the raw retention stays NULL
-- and is measured from created_at — it produced nothing for a day, which is
-- exactly what the sweep exists to report.
-- ==========================================================================

alter table checks add column if not exists last_result_at timestamptz;

--bun:split

comment on column checks.last_result_at is
  'Execution time of the newest real result (up/down/timeout/error/warning) across every region. NULL when the check never produced one. Read by the freshness sweep (spec 2026-09-25-02).';

--bun:split

update checks c
   set last_result_at = (
     select max(r.period_start)
       from results r
      where r.organization_uid = c.organization_uid
        and r.check_uid = c.uid
        and r.period_type = 'raw'
        and r.status in (3, 4, 5, 6, 8)
   )
 where c.deleted_at is null;

--bun:split

-- The sweep's candidate predicate is
--   coalesce(last_result_at, created_at) < cutoff
-- over enabled, non-internal, live checks that are not already stale.
create index if not exists idx_checks_freshness
  on checks ((coalesce(last_result_at, created_at)))
  where deleted_at is null and enabled and not internal and status <> 10;

--bun:split

-- The column comment written in 001 predates every status after `down`.
comment on column checks.status is
  'Current derived check status: 1=created, 3=up, 4=down, 5=validating, 7=degraded (aggregated only), 8=warning, 10=stale (no recent real result).';

--bun:split

-- ==========================================================================
-- SECTION: passive-checks-no-regions  (spec 2026-09-25-04)
--
-- Passive checks (heartbeat, email) make no outbound request, so a region
-- added nothing but a place for their evaluator to die: a dark region
-- silenced every dead-man's switch pinned to it, and a region served by an
-- agent turned every evaluation into an error and an incident. They are now
-- evaluated on the jobs node from ONE job with a NULL region.
--
-- The type list mirrors checkerdef.PassiveCheckTypes().
--
-- The surviving job is converted rather than inserted so its scheduled_at
-- and plan weight carry over. Anything this leaves behind (a passive check
-- with no job at all) is healed by the boot repair
-- (ListChecksWithStaleJobRegions), which runs after migrations at every start.
-- ==========================================================================

update checks
   set regions = '{}'
 where type in ('heartbeat', 'email')
   and coalesce(array_length(regions, 1), 0) > 0;

--bun:split

-- A passive check that already owns its NULL-region job loses every regional
-- one.
delete from check_jobs cj
 where cj.type in ('heartbeat', 'email')
   and cj.region is not null
   and exists (
     select 1 from check_jobs n
      where n.check_uid = cj.check_uid
        and n.region is null
   );

--bun:split

-- Otherwise keep exactly one regional job per check (the lowest uid)...
delete from check_jobs cj
 where cj.type in ('heartbeat', 'email')
   and cj.region is not null
   and cj.uid::text <> (
     select min(k.uid::text) from check_jobs k
      where k.check_uid = cj.check_uid
        and k.region is not null
   );

--bun:split

-- ...and turn it into the NULL-region job, lease cleared so the jobs node can
-- claim it on its next pass.
update check_jobs
   set region = null,
       lease_worker_uid = null,
       lease_expires_at = null,
       lease_starts = 0,
       updated_at = now()
 where type in ('heartbeat', 'email')
   and region is not null;

--bun:split

-- ==========================================================================
-- SECTION: auto-region-placement  (spec 2026-09-25-06)
--
-- Regions were resolved once and frozen with no record of intent: every new
-- check was pinned to the org/system default regions, whether anybody chose
-- them or not, so one dark region stopped every check "pinned" there. A check
-- now records its placement intent:
--
--   placement     'pinned' (regions is the user's list, never moved) or
--                 'auto'   (regions is the CURRENT placement, rewritten by
--                           the region sweep when a placed region goes dark)
--   region_count  auto only: N, how many regions run the check
--   region_pool   auto only: candidate cloud slugs, NULL/empty = any
--
-- Keeping the auto placement in checks.regions is what keeps the boot repair,
-- reconcileCheckJobs, the phase computation and the rate accounting unchanged.
-- ==========================================================================

alter table checks add column if not exists placement text not null default 'pinned';

--bun:split

alter table checks drop constraint if exists checks_placement_valid;

--bun:split

alter table checks add constraint checks_placement_valid check (placement in ('pinned', 'auto'));

--bun:split

alter table checks add column if not exists region_count integer;

--bun:split

alter table checks add column if not exists region_pool text[];

--bun:split

comment on column checks.placement is
  'Placement intent: pinned (regions is the user''s explicit list, never moved) or auto (regions is the current placement, re-placed by the region sweep when a placed region goes dark). Spec 2026-09-25-06.';

--bun:split

comment on column checks.region_count is
  'Auto placement only: how many regions run the check. NULL for pinned checks.';

--bun:split

comment on column checks.region_pool is
  'Auto placement only: candidate cloud region slugs; NULL or empty means any cloud region.';

--bun:split

-- The one migration exception (resolved open question 1): a check whose
-- regions are exactly the system default_regions, as a set, was almost
-- certainly never placed by anybody — it is the "67 checks pinned to
-- gravelines" case. It becomes auto with region_count = its current region
-- count and an empty pool: same regions, same jobs, same cost, and it gains
-- failover. Every other check keeps placement = 'pinned' (the column default).
-- Passive types (checkerdef.PassiveCheckTypes) have no regions and never move;
-- a check naming a private (`@`) region is pinned-only.
update checks c
   set placement = 'auto',
       region_count = cardinality(c.regions),
       region_pool = null
  from parameters p
 where p.organization_uid is null
   and p.key = 'default_regions'
   and p.deleted_at is null
   and jsonb_typeof(p.value -> 'value') = 'array'
   and c.deleted_at is null
   and c.placement = 'pinned'
   and c.type not in ('heartbeat', 'email', 'private-location')
   and cardinality(c.regions) > 0
   and not exists (select 1 from unnest(c.regions) as r(slug) where r.slug like '@%')
   and cardinality(c.regions) = (select count(distinct x.slug) from unnest(c.regions) as x(slug))
   and (select array_agg(distinct x.slug order by x.slug) from unnest(c.regions) as x(slug))
     = (select array_agg(distinct d.slug order by d.slug)
          from jsonb_array_elements_text(p.value -> 'value') as d(slug));
