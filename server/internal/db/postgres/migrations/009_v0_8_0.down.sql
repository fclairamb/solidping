-- Teardown/parity only — never run in production. Reverses 009_v0_8_0.up.sql.
--
-- Restores the dropped columns and the last_for_status partial index. No
-- backfill is performed or needed: availability_pct is recomputable at any time
-- from successful_checks / total_checks, and last_for_status is repopulated by
-- the next result insert (the feature it fed has been removed anyway).

alter table results add column last_for_status boolean;
alter table results add column availability_pct double precision;

create index idx_results_last_for_status on results (check_uid, status) where last_for_status = true;

comment on column results.last_for_status is 'Marks the most recent result per check+status combination (raw only).';
comment on column results.availability_pct is 'Uptime percentage for this period (aggregated only).';
