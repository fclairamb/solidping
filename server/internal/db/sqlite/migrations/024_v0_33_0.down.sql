-- Teardown/parity half of the consolidated v0.33.0 SQLite migration — never
-- run in production. Sections appear in the EXACT REVERSE order of
-- 024_v0_33_0.up.sql.

-- ==========================================================================
-- SECTION: check-freshness
-- ==========================================================================

drop index if exists idx_checks_freshness;

--bun:split

update checks set status = 1 where status = 10;

--bun:split

alter table checks drop column last_result_at;
