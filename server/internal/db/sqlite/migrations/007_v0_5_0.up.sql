-- solidping v0.5.0 — purge FK-orphan results rows so aggregation self-heals
-- (spec 2026-07-12-01 §3).
--
-- SQLite foreign keys are per-connection: any connection re-opened by
-- database/sql after an error (or an external sqlite3 CLI session) runs with
-- FKs OFF, so a hard delete of a check could skip the ON DELETE CASCADE on
-- results.check_uid and leave orphaned rows behind. Those orphans are a
-- deterministic poison pill — the aggregation rollup INSERT fails the
-- results.check_uid → checks.uid foreign key (SQLite error 787) and permanently
-- halts the org's aggregation.
--
-- Delete every results row whose check no longer exists (soft-deleted checks
-- still have a row and are preserved). Idempotent / no-op on a clean database.
-- Pairs with the code fix: work-discovery now skips orphaned buckets and the
-- job always reschedules its follow-up, so future externally-created orphans
-- are inert dead rows rather than poison pills. Mirrors the Postgres 007
-- migration (sync-pg-to-sqlite); on Postgres it is a no-op by FK construction.
delete from results
where check_uid not in (select uid from checks);
