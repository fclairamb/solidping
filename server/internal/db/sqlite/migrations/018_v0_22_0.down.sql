-- Teardown/parity half of the consolidated v0.22.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of
-- 018_v0_22_0.up.sql.

-- ==========================================================================
-- SECTION: heartbeat-push-counters
-- ==========================================================================

drop table if exists heartbeat_counters;
