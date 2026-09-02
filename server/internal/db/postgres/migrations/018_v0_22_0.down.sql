-- Teardown/parity half of the consolidated v0.22.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of
-- 018_v0_22_0.up.sql.

-- ==========================================================================
-- SECTION: passive-signal-index
--
-- Dropping the index costs nothing but performance: a downgraded binary asks
-- for the newest raw row of any origin again, which results_raw_idx already
-- serves.
-- ==========================================================================

drop index if exists results_raw_signal_idx;
