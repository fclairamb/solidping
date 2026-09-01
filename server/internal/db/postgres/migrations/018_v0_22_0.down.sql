-- Teardown/parity half of the consolidated v0.22.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of
-- 018_v0_22_0.up.sql.

-- ==========================================================================
-- SECTION: heartbeat-push-counters
--
-- Dropping the table discards every remembered SP2 counter. A downgraded
-- binary has never heard of the push transport, so nothing reads them; if the
-- binary is later upgraded again, the first signed beat from each device is
-- accepted at whatever counter it carries and monotonicity resumes from
-- there. That window is exactly what the SP2 `ts` freshness check exists to
-- bound.
-- ==========================================================================

drop table if exists heartbeat_counters;
