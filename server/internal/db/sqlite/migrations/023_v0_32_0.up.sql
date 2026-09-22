-- v0.32.0 — the ONE consolidated SQLite migration for the (still unreleased)
-- v0.32.0 release. 022_v0_30_0 shipped, so this is the next free number and
-- every schema change of this cycle is appended here as a new SECTION, per
-- wiki/conventions/database.md. Mirrors the Postgres twin (023_v0_32_0) section
-- for section.
--
--   SECTION: degraded-detection   the per-check degraded-detection rules, the
--                                 dry-run stamp, the status-page opt-in and the
--                                 active-degraded-incident dedup index
--
-- ⚠️ A DEV DATABASE THAT ALREADY RAN AN EARLIER DRAFT OF THIS FILE MUST BE
-- RESET, NEVER REPAIRED. bun keys an applied migration on its numeric prefix
-- alone, so appending a section to 023 does not re-run it: set
-- `SP_DB_RESET=true` (test/demo run mode) or delete the SQLite file.
-- **Do not run `solidping migrate repair`** — it rewrites the recorded
-- checksum without applying anything.

-- ==========================================================================
-- SECTION: degraded-detection  (spec 2026-09-22-03)
--
-- See the Postgres twin for the full rationale: one rule primitive ("M of the
-- last N countable probes match") over two populations, off for every existing
-- check (degraded_enabled defaults to false — upgrading must never start paging
-- on its own) and adopted through a dry run that stamps degraded_would_fire_at.
-- degraded_evaluated_at is evaluator rotation state, not configuration.
--
-- SQLite has no `add column if not exists`; these run once on a database that
-- has never seen 023.
-- ==========================================================================

alter table checks add column degraded_failures integer not null default 5;
alter table checks add column degraded_failures_window integer not null default 60;
alter table checks add column degraded_slow integer not null default 3;
alter table checks add column degraded_slow_window integer not null default 6;
alter table checks add column slow_threshold_ms integer not null default 0;
alter table checks add column degraded_enabled boolean not null default false;
alter table checks add column degraded_would_fire_at text;
alter table checks add column degraded_evaluated_at text;

--bun:split

-- Degraded incidents are not auto-published; opt in per page.
alter table status_pages add column publish_degraded boolean not null default false;

--bun:split

-- Dedup enforced by the database: at most ONE open degraded incident per check.
-- See the Postgres file for the racing-replica rationale.
create unique index if not exists uq_active_degraded_incident
  on incidents (check_uid)
  where state = 1 and kind = 'degraded' and deleted_at is null;

--bun:split

create index if not exists idx_checks_degraded_eval
  on checks (degraded_evaluated_at)
  where deleted_at is null and enabled;
