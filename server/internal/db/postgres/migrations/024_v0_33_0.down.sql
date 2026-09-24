-- Teardown/parity half of the consolidated v0.33.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of
-- 024_v0_33_0.up.sql.

-- ==========================================================================
-- SECTION: drop-degraded-dry-run
--
-- The column comes back empty (the stamps are not recoverable) and the sweep
-- index regains its 023 predicate. Incidents closed as 'disabled' stay closed.
-- ==========================================================================

drop index if exists idx_checks_degraded_eval;

--bun:split

create index if not exists idx_checks_degraded_eval
  on checks (degraded_evaluated_at)
  where deleted_at is null and enabled;

--bun:split

comment on column checks.degraded_enabled is
  'Whether degraded detection may OPEN incidents on this check. FALSE = dry run (stamps degraded_would_fire_at only).';

--bun:split

alter table checks add column if not exists degraded_would_fire_at timestamptz;

--bun:split

-- ==========================================================================
-- SECTION: degraded-incident-kind
--
-- Back to the 015 constraint, NOT VALID so existing degraded rows do not block
-- the downgrade.
-- ==========================================================================

alter table incidents drop constraint if exists incidents_kind_check;

--bun:split

alter table incidents add constraint incidents_kind_check check (kind in ('check', 'slo_burn')) not valid;

--bun:split

-- ==========================================================================
-- SECTION: auto-region-placement
--
-- The placement columns go; every check keeps its regions (the placement
-- the scheduler last wrote), which the previous schema reads as pinned.
-- ==========================================================================

alter table checks drop constraint if exists checks_placement_valid;

--bun:split

alter table checks drop column if exists region_pool;

--bun:split

alter table checks drop column if exists region_count;

--bun:split

alter table checks drop column if exists placement;

--bun:split

-- ==========================================================================
-- SECTION: passive-checks-no-regions
--
-- Nothing to undo: the up section is a data normalization (passive checks
-- lose their regions and keep one NULL-region job), which the previous schema
-- accepts as is.
-- ==========================================================================

-- ==========================================================================
-- SECTION: check-freshness
-- ==========================================================================

drop index if exists idx_checks_freshness;

--bun:split

-- A stale check has no status in the previous schema's vocabulary; `created`
-- (no usable data) is the honest downgrade.
update checks set status = 1 where status = 10;

--bun:split

alter table checks drop column if exists last_result_at;
