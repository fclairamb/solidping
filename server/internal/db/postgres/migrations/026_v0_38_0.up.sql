-- v0.38.0 — the ONE consolidated Postgres migration for the (still unreleased)
-- v0.38.0 release. 025_v0_34_0 shipped and is frozen, so this is the next free
-- number; every schema change of this cycle is appended here as a SECTION.
--
--   SECTION: results-duration-p50
--                              results.duration_p50: the median duration
--
-- Additive and re-runnable (nullable column, `if not exists`).

alter table results add column if not exists duration_p50 real;

--bun:split

comment on column results.duration_p50 is
  'Median (nearest-rank p50) duration in milliseconds of the probes this row summarizes. NULL on rows that predate the column or carry no measured duration; never backfilled, and distinct from a real 0.';
