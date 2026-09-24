-- Teardown/parity half of the consolidated v0.33.0 SQLite migration — never
-- run in production. Sections appear in the EXACT REVERSE order of
-- 024_v0_33_0.up.sql.

-- ==========================================================================
-- SECTION: drop-degraded-dry-run
--
-- The column comes back empty and the sweep index regains its 023 predicate.
-- ==========================================================================

drop index if exists idx_checks_degraded_eval;

--bun:split

create index if not exists idx_checks_degraded_eval
  on checks (degraded_evaluated_at)
  where deleted_at is null and enabled;

--bun:split

alter table checks add column degraded_would_fire_at text;

--bun:split

-- ==========================================================================
-- SECTION: degraded-incident-kind
--
-- Nothing to undo on SQLite.
-- ==========================================================================

-- ==========================================================================
-- SECTION: auto-region-placement
--
-- The placement columns go; every check keeps its regions (the placement
-- the scheduler last wrote), which the previous schema reads as pinned.
-- ==========================================================================

alter table checks drop column region_pool;

--bun:split

alter table checks drop column region_count;

--bun:split

alter table checks drop column placement;

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

update checks set status = 1 where status = 10;

--bun:split

alter table checks drop column last_result_at;
