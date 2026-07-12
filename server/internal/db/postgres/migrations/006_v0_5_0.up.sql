-- solidping v0.5.0 — make aggregated-result writes idempotent by closing the
-- NULL-region hole in results_aggregated_unique_idx (spec 2026-07-11-16 §3).
--
-- The old index on (organization_uid, check_uid, region, period_type,
-- period_start) never fired for region IS NULL rows — Postgres treats NULLs as
-- distinct — so the aggregation poison-pill loop duplicated 'hour' rows
-- unbounded. Dedupe the existing non-raw rows first (keep the best survivor per
-- bucket: highest total_checks, tie-break earliest created_at, then uid), then
-- rebuild the index over coalesce(region, ''). Idempotent / no-op on a clean
-- database. This is also how a polluted database (e.g. solidping_dev) gets its
-- duplicate rollup rows removed on deploy.
delete from results
where uid in (
  select uid from (
    select uid,
      row_number() over (
        partition by organization_uid, check_uid, coalesce(region, ''), period_type, period_start
        order by coalesce(total_checks, -1) desc, created_at asc, uid asc
      ) as rn
    from results
    where period_type != 'raw'
  ) ranked
  where rn > 1
);

drop index if exists results_aggregated_unique_idx;

create unique index results_aggregated_unique_idx
  on results (organization_uid, check_uid, coalesce(region, ''), period_type, period_start)
  where period_type != 'raw';
