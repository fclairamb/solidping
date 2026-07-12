-- solidping v0.5.0 — purge FK-orphan results rows so aggregation self-heals
-- (spec 2026-07-12-01 §3).
--
-- Postgres enforces results.check_uid → checks.uid at the schema level, so it
-- cannot actually hold orphaned rows and this delete is a no-op. It is shipped
-- for symmetry with the SQLite 007 migration (sync-pg-to-sqlite), where
-- per-connection FK enforcement can be bypassed and orphaned rows do occur —
-- those orphans are a deterministic poison pill that fails the aggregation
-- rollup INSERT and permanently halts the org's aggregation. Idempotent.
delete from results
where check_uid not in (select uid from checks);
