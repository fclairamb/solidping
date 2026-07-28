-- Teardown/parity only — never run in production. Reverses 009_v0_8_0.up.sql.
--
-- Restores the dropped columns and the last_for_status partial index. No
-- backfill: availability_pct is recomputable from successful_checks /
-- total_checks, and last_for_status is repopulated by the next result insert.

alter table results add column last_for_status integer;
alter table results add column availability_pct real;

create index idx_results_last_for_status on results (check_uid, status) where last_for_status = 1;
