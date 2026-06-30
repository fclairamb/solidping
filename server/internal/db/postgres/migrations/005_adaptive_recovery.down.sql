-- Rollback for the adaptive-recovery flapping columns (spec 2026-06-30-07).
-- Re-adds the dropped (dead) max_adaptive_increase column and drops the five
-- new columns.

alter table checks add column max_adaptive_increase integer;

comment on column checks.max_adaptive_increase is 'Maximum multiplier for adaptive resolution increase. NULL uses system default.';

alter table checks drop column if exists last_outage_at;
alter table checks drop column if exists flap_count;
alter table checks drop column if exists max_recovery_multiplier;
alter table checks drop column if exists flap_backoff_factor;
alter table checks drop column if exists flapping_window_seconds;
