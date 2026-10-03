-- v0.38.0 — SQLite twin of the Postgres 026_v0_38_0 migration.
--
-- SECTION: results-duration-p50
-- SQLite has no `add column if not exists`; this runs once on a database that
-- has never seen 026. The column is nullable, so the add is metadata-only.

alter table results add column duration_p50 real; -- Median (nearest-rank p50) duration in ms. NULL on rows that predate the column or carry no measured duration
