-- v0.33.0 — the ONE consolidated Postgres migration for the (still unreleased)
-- v0.33.0 release. 023_v0_32_0 shipped, so this is the next free number and
-- every schema change of this cycle is appended here as a new SECTION, per
-- wiki/conventions/database.md.
--
--   SECTION: check-freshness   checks.last_result_at, its backfill and the
--                              freshness-sweep index
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
