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
