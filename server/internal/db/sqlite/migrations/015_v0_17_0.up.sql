-- SQLite mirror of postgres/migrations/015_v0_17_0.up.sql — reap abandoned
-- created/running raw results (spec 2026-08-18-03). See the Postgres file for
-- why the column exists and why the reaper needs its own index.

alter table results add column abandoned integer not null default 0; -- True (1) when the abandoned-result reaper finalized this row from a stale created/running marker: status becomes error, but the row is excluded from availability unlike a genuine error.

create index idx_results_lifecycle_pending on results (period_start)
  where period_type = 'raw' and status in (1, 2);
