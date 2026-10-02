-- Teardown/parity half of 026_v0_38_0.up.sql — never run in production.

-- ==========================================================================
-- SECTION: results-duration-p50
-- ==========================================================================

alter table results drop column if exists duration_p50;
