-- Migration 012 down: deliberately a NO-OP.
--
-- The up migration rewrites `@<org-slug>/<region-slug>` to `@<region-slug>` and
-- de-duplicates the rows that collapse onto one another. That is LOSSY in both
-- directions: the org slug is gone from the string (it lived nowhere else), and
-- the de-duplicated rows are gone. Re-deriving `@<org>/<slug>` from the row's
-- organization_uid would produce the org's CURRENT slug, which is exactly the
-- stale-copy bug this migration exists to remove — a "rollback" would silently
-- re-strand every agent enrolled before a rename.
--
-- Rolling back the code without rolling back the data is safe: the old code
-- reads `agents.region` and `checks.regions` verbatim and matches them on exact
-- equality, so an install left on `@<slug>` on both sides keeps working.

select 1;
