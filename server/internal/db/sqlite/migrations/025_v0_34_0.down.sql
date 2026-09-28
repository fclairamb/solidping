-- Teardown/parity half of the consolidated v0.34.0 SQLite migration — never
-- run in production. Sections appear in the EXACT REVERSE order of
-- 025_v0_34_0.up.sql.

-- ==========================================================================
-- SECTION: capture-now-failure
-- ==========================================================================

alter table check_jobs drop column capture_failure_reason;

--bun:split

alter table check_jobs drop column capture_failed_request_at;
