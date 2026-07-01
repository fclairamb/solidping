-- solidping — fast/slow check lanes (spec 2026-07-01-03).
--
-- Ordering assigns free slots; it cannot free occupied ones: once slow probes
-- hold every runner goroutine, no ORDER BY helps a due fast check. This adds
-- the storage half of the reserved-capacity fix: a lane smallint (0 = fast,
-- 1 = slow) classified from the cost EWMA with hysteresis in the post-exec
-- write, and per-lane partial indexes so the claim's two lane-filtered SELECTs
-- (fast first, slow bounded by the reservation budget) stay index-backed.
--
-- Index shape (D5): two partial indexes on effective_scheduled_at (one per
-- lane) were benchmarked against a single composite
-- (lane, effective_scheduled_at) via make bench-checks — see the results note
-- at the bottom of this file. The partial shape is kept: each lane SELECT
-- scans an index containing only its own lane's rows (no lane-prefix
-- traversal), and the two indexes together are no larger than the composite.

alter table check_jobs add column lane smallint not null default 0;

-- Backfill at the default promote threshold (scheduling.lane_slow_threshold_ms
-- = 2000). A mismatch with a non-default config self-heals: one execution
-- reclassifies the job through the hysteresis band.
update check_jobs set lane = 1 where cost_ewma_ms >= 2000;

create index if not exists idx_check_jobs_claim_fast
    on check_jobs (effective_scheduled_at) where lane = 0;
create index if not exists idx_check_jobs_claim_slow
    on check_jobs (effective_scheduled_at) where lane = 1;

-- The full-table ordering index is superseded by the two partial ones: every
-- claim SELECT now filters on lane, so the un-partitioned index would never
-- be preferred again.
drop index if exists idx_check_jobs_effective_scheduled_at;

comment on column check_jobs.lane is 'Scheduling lane: 0 = fast, 1 = slow. Classified from cost_ewma_ms with hysteresis (promote >= lane_slow_threshold_ms, demote < lane_fast_threshold_ms) in the post-exec release. Slow jobs are capped at pool_size − fast_lane_reserved in-flight per worker; fast jobs may use any slot.';

-- D5 bench (2026-07-02, make bench-checks BENCH_DURATION=1m, 200 checks @10s):
-- partial vs composite claim p95 was equivalent within run-to-run noise on
-- both backends (see spec 2026-07-01-03 for the recorded numbers); the
-- partial shape is kept per the tie-breaker above.
