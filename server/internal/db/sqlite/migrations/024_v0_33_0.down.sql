-- Teardown/parity half of the consolidated v0.33.0 SQLite migration — never
-- run in production. Sections appear in the EXACT REVERSE order of
-- 024_v0_33_0.up.sql.

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
