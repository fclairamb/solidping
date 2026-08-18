-- SQLite has no `DROP COLUMN IF EXISTS` in the version bun targets here, but
-- plain `DROP COLUMN` (3.35+) is fine — this mirrors every other down
-- migration in this directory that drops a column it added.
drop index if exists idx_results_lifecycle_pending;

alter table results drop column abandoned;
