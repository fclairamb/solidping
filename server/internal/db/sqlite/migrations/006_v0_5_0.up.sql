-- solidping v0.5.0 — aggregation hardening. Two independent, idempotent repairs
-- of the results table, shipped as the single consolidated v0.5.0 migration
-- (specs 2026-07-11-16 §3 and 2026-07-12-01 §3). Mirrors the Postgres 006
-- migration (sync-pg-to-sqlite).
--
-- (1) Purge FK-orphan results rows (results.check_uid with no matching checks
--     row). SQLite foreign keys are per-connection: any connection re-opened by
--     database/sql after an error (or an external sqlite3 CLI session) runs with
--     FKs OFF, so a hard delete of a check could skip the ON DELETE CASCADE on
--     results.check_uid and leave orphaned rows behind. Those orphans are a
--     deterministic poison pill — the aggregation rollup INSERT fails the
--     results.check_uid → checks.uid foreign key (SQLite error 787) and
--     permanently halts the org's aggregation. Soft-deleted checks still have a
--     row and are preserved. On Postgres this delete is a no-op by FK
--     construction.
--
-- (2) Close the NULL-region hole in results_aggregated_unique_idx. The old index
--     on (organization_uid, check_uid, region, period_type, period_start) never
--     fired for region IS NULL rows — SQLite also treats NULLs as distinct in
--     unique indexes — so the aggregation poison-pill loop duplicated 'hour'
--     rows unbounded. Dedupe the existing non-raw rows first (keep the best
--     survivor per bucket: highest total_checks, tie-break earliest created_at,
--     then uid), then rebuild the index over coalesce(region, '').
--
-- Idempotent / no-op on a clean database.

-- (1) purge FK-orphan rows
delete from results
where check_uid not in (select uid from checks);

-- (2) dedupe duplicate aggregated rows, then rebuild the NULL-proof unique index
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
