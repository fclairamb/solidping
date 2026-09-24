-- Teardown/parity half of the consolidated v0.33.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of
-- 024_v0_33_0.up.sql.

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
