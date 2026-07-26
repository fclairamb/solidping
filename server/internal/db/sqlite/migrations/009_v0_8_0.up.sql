-- solidping v0.8.0 — Results table tier-1 storage trim (spec 2026-07-24-02).
-- See the Postgres 009 up migration for the full rationale.
--
-- SQLite >=3.35 (the bundled driver is well past that) supports
-- ALTER TABLE ... DROP COLUMN. The partial index idx_results_last_for_status
-- references last_for_status in its WHERE clause, so it must be dropped before
-- the column or SQLite rejects the DROP COLUMN. availability_pct is in no
-- index and drops directly.

drop index if exists idx_results_last_for_status;
alter table results drop column last_for_status;
alter table results drop column availability_pct;
