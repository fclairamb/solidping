-- solidping v0.8.0 — Results table tier-1 storage trim (spec 2026-07-24-02).
--
-- The `results` table is the largest and hottest table in the system. Two of
-- its columns are pure waste and are dropped here:
--
--   * last_for_status — a write-only flag. Every result insert ran a companion
--     UPDATE clearing the predecessor's flag (dead tuple + index churn + WAL
--     per row), yet nothing ever read the column: the dashboard's "latest per
--     check" uses DISTINCT ON (check_uid), and the only WHERE last_for_status
--     was the maintenance UPDATE itself. Its partial index goes with it. Drop
--     the index first — Postgres rejects dropping a column an index still
--     references.
--
--   * availability_pct — fully derivable as successful_checks / total_checks
--     × 100, now computed at read time (handlers/results, availability, badges).
--     Storing it was redundant and one more way for the two to disagree.

drop index if exists idx_results_last_for_status;
alter table results drop column last_for_status;
alter table results drop column availability_pct;
