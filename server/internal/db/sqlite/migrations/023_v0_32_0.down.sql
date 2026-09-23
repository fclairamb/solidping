-- Teardown/parity half of the consolidated v0.32.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of
-- 023_v0_32_0.up.sql.

-- ==========================================================================
-- SECTION: degraded-detection
--
-- SQLite has supported `ALTER TABLE ... DROP COLUMN` since 3.35. The indexed
-- column (degraded_evaluated_at) has its index dropped first, which is what
-- lets the drop proceed without a table rebuild.
-- ==========================================================================

drop index if exists idx_checks_degraded_eval;

drop index if exists uq_active_degraded_incident;

delete from incidents where kind = 'degraded';

alter table status_pages drop column publish_degraded;

alter table checks drop column degraded_evaluated_at;
alter table checks drop column degraded_would_fire_at;
alter table checks drop column degraded_enabled;
alter table checks drop column slow_threshold_ms;
alter table checks drop column degraded_slow_window;
alter table checks drop column degraded_slow;
alter table checks drop column degraded_failures_window;
alter table checks drop column degraded_failures;
