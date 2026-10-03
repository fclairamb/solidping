-- v0.38.0 — SQLite twin of the Postgres 026_v0_38_0 migration.
--
-- SECTION: results-duration-p50
-- SQLite has no `add column if not exists`; this runs once on a database that
-- has never seen 026. The column is nullable, so the add is metadata-only.

alter table results add column duration_p50 real; -- Median (nearest-rank p50) duration in ms. NULL on rows that predate the column or carry no measured duration

--bun:split

-- SECTION: multistep-bulk-checks (spec 2026-10-03-03)
-- Pointers to the multi-step run in progress (the crawl) on the bulk lane
-- (lane = 2). All NULL / 0 when idle.

alter table check_jobs add column step_run_uid text; -- uuid of the run in progress, NULL when idle
--bun:split
alter table check_jobs add column step_run_started_at text; -- start of the run: deadline and next-period anchor
--bun:split
alter table check_jobs add column step_count integer not null default 0; -- slices completed by the run
--bun:split
alter table check_jobs add column step_state_file_uid text; -- files.uid of the CURRENT state, NULL on the first slice
--bun:split
alter table check_jobs add column step_failures integer not null default 0; -- consecutive failed slices

--bun:split

create index if not exists idx_check_jobs_claim_bulk
    on check_jobs (effective_scheduled_at) where lane = 2;
