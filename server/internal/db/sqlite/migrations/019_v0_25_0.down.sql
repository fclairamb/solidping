-- Teardown/parity half of the consolidated v0.25.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of
-- 019_v0_25_0.up.sql.

-- ==========================================================================
-- SECTION: demo-account
-- ==========================================================================

alter table checks drop column created_by;

--bun:split

alter table users drop column demo;
