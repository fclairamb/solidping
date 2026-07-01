-- solidping — fast/slow check lanes (spec 2026-07-01-03).
-- SQLite mirror of the Postgres migration.
--
-- Ordering assigns free slots; it cannot free occupied ones: once slow probes
-- hold every runner goroutine, no ORDER BY helps a due fast check. This adds
-- the storage half of the reserved-capacity fix: a lane smallint (0 = fast,
-- 1 = slow) classified from the cost EWMA with hysteresis in the post-exec
-- write, and per-lane partial indexes (SQLite supports partial indexes) so the
-- claim's two lane-filtered SELECTs stay index-backed.
--
-- Index shape (D5): two partial indexes on effective_scheduled_at (one per
-- lane) were benchmarked against a single composite
-- (lane, effective_scheduled_at) via make bench-checks — see the results note
-- at the bottom of this file. The partial shape is kept: each lane SELECT
-- scans an index containing only its own lane's rows.

-- lane: 0 = fast, 1 = slow — classified from cost_ewma_ms with hysteresis in
-- the post-exec release.
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
-- claim SELECT now filters on lane.
drop index if exists idx_check_jobs_effective_scheduled_at;

-- D5 bench (2026-07-02, make bench-checks, 200 checks @ 10s, fetch-stage =
-- ClaimJobs wall clock): partial vs composite (lane, effective_scheduled_at)
-- is equivalent within run-to-run noise — 1m runs measured fetch p50/p95 of
-- 3.5/33.4ms then 1.8/8.3ms (partial, two runs) vs 3.1/10.0ms (composite) on
-- SQLite, and 4.8/41.0ms then 0.98/21.5ms (partial) vs 1.3/9.7ms (composite)
-- on embedded PG; the inter-run spread exceeds the inter-shape delta. The
-- partial shape is kept per the tie-breaker above. No regression vs the
-- pre-lane baseline (2m runs): SQLite fetch p50/p95 3.2/10.0ms vs 3.9/33.7ms
-- before, PG 0.96/8.8ms vs 1.3/9.0ms before.
