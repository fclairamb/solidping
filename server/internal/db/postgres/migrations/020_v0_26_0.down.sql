-- Teardown/parity half of the consolidated v0.26.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of
-- 020_v0_26_0.up.sql.

-- ==========================================================================
-- SECTION: signup-attribution
-- ==========================================================================

alter table users drop column if exists signup_attribution;
