-- SQLite mirror of postgres/migrations/016_v0_17_0.up.sql — replace the
-- `results.abandoned` boolean with the dedicated ResultStatusAbandoned (9)
-- status value (spec 2026-08-18-10). See the Postgres file for why the flag
-- collapses into the status enum, and why this is a new migration rather than
-- an edit of the already-applied 015.
--
-- Order matters: convert first, drop second. `abandoned = 1` (SQLite stores
-- the boolean as an integer) is what keeps the backfill from sweeping genuine
-- `error` rows, which must keep counting against availability.

update results
set status = 9
where period_type = 'raw'
  and status = 6
  and abandoned = 1;

-- SQLite has no `DROP COLUMN IF EXISTS`, but plain `DROP COLUMN` (3.35+) is
-- fine — the same form 015's own down migration uses.
alter table results drop column abandoned;
