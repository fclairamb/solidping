-- Teardown/parity half of 027_v0_39_0.up.sql — never run in production.

-- ==========================================================================
-- SECTION: check-deleted-incidents
-- ==========================================================================

-- The backfill is a data fix with no schema change: resolved orphans stay
-- resolved (re-opening them would restart their escalations).
select 1;
