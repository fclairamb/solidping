-- Teardown/parity half of the consolidated v0.32.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of
-- 023_v0_32_0.up.sql.

-- ==========================================================================
-- SECTION: degraded-detection
-- ==========================================================================

drop index if exists idx_checks_degraded_eval;

drop index if exists uq_active_degraded_incident;

-- Incidents of a kind the schema no longer knows about would be orphaned rows
-- nothing can render, exactly as the slo_burn teardown reasons about them.
delete from incidents where kind = 'degraded';

alter table status_pages drop column if exists publish_degraded;

alter table checks drop column if exists degraded_evaluated_at;
alter table checks drop column if exists degraded_would_fire_at;
alter table checks drop column if exists degraded_enabled;
alter table checks drop column if exists slow_threshold_ms;
alter table checks drop column if exists degraded_slow_window;
alter table checks drop column if exists degraded_slow;
alter table checks drop column if exists degraded_failures_window;
alter table checks drop column if exists degraded_failures;
