-- SQLite mirror of postgres/migrations/016_worker_version.up.sql — workers
-- self-report their own build version (spec 2026-08-19-07). See the Postgres
-- file for the full rationale: NULL-only-unknown, no backfill, and why this
-- is a two-state column (scalar) rather than three-state (set) the way
-- `capabilities` is.
--
-- SQLite has no `ADD COLUMN IF NOT EXISTS`, so this is not re-runnable —
-- same as every other scratch migration this cycle. No triggers are needed
-- here (unlike `capabilities`): a plain column CHECK is enough to validate a
-- single scalar, where the array-element rules for `capabilities` needed
-- `json_each` triggers because SQLite forbids subqueries in a CHECK and
-- json_each is table-valued.
--
-- Numbering: scratch migration for the still-unreleased v0.17.0 cycle (013
-- is the last RELEASED migration; 014_v0_17_0 is that cycle's consolidated
-- file so far; 015 removed the Opsgenie integration type). Folds into
-- 014_v0_17_0 at release consolidation.

alter table workers add column version text
  check (version is null or version <> '');
