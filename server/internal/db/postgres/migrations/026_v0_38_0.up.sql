-- v0.38.0 — the ONE consolidated Postgres migration for the (still unreleased)
-- v0.38.0 release. 025_v0_34_0 shipped and is frozen, so this is the next free
-- number; every schema change of this cycle is appended here as a SECTION.
--
--   SECTION: results-duration-p50
--                              results.duration_p50: the median duration
--   SECTION: multistep-bulk-checks
--                              check_jobs.step_*: resumable multi-step runs on
--                              the bulk lane (lane = 2)
--
-- Additive and re-runnable (nullable column, `if not exists`).

alter table results add column if not exists duration_p50 real;

--bun:split

comment on column results.duration_p50 is
  'Median (nearest-rank p50) duration in milliseconds of the probes this row summarizes. NULL on rows that predate the column or carry no measured duration; never backfilled, and distinct from a real 0.';

--bun:split

-- SECTION: multistep-bulk-checks (spec 2026-10-03-03)
-- A multi-step check (the website crawl) runs as a series of short slices on
-- a third claim lane, bulk (lane = 2). These columns point at the run in
-- progress; its state lives in a `checks/<uid>/step-state` attachment. All
-- NULL / 0 when no run is in progress. Additive and re-runnable.

alter table check_jobs add column if not exists step_run_uid text;
alter table check_jobs add column if not exists step_run_started_at timestamptz;
alter table check_jobs add column if not exists step_count int not null default 0;
alter table check_jobs add column if not exists step_state_file_uid text;
alter table check_jobs add column if not exists step_failures int not null default 0;

--bun:split

create index if not exists idx_check_jobs_claim_bulk
    on check_jobs (effective_scheduled_at) where lane = 2;

--bun:split

comment on column check_jobs.step_run_uid is
  'UUID of the multi-step run in progress (spec 2026-10-03-03), NULL when idle. Slice writes are fenced on it.';
comment on column check_jobs.step_run_started_at is
  'Start of the multi-step run in progress: the maxRunDuration deadline and the next-period anchor.';
comment on column check_jobs.step_count is
  'Slices completed by the multi-step run in progress.';
comment on column check_jobs.step_state_file_uid is
  'files.uid of the CURRENT step-state attachment; NULL on the first slice. The two newest state files are kept.';
comment on column check_jobs.step_failures is
  'Consecutive failed slices of the run in progress; three end the run with an error result.';
