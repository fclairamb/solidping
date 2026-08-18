-- SQLite mirror of postgres/migrations/014_v0_17_0.up.sql — the
-- abandoned-result reaper: the dedicated ResultStatusAbandoned (9) status
-- value plus the partial index the reaper scans on (specs 2026-08-18-03 and
-- 2026-08-18-10, consolidated). See the Postgres file for why the reaper
-- exists, why only status=1 (created) is reaped and never status=2 (running,
-- used by heartbeat checks for a legitimate long-lived report), why the state
-- is a status rather than a boolean flag alongside `error`, why a reaped
-- attempt is excluded from availability, and why this is numbered 014.
--
-- Postgres widens the CHECK domain with a DROP CONSTRAINT / ADD CONSTRAINT
-- pair. SQLite has neither DROP CONSTRAINT nor ALTER COLUMN, and 001 declares
-- `status integer check (status in (0, ..., 8))` inline, so the table has to be
-- rebuilt with the established *_new pattern (same technique as 005_v0_4_0 and
-- 009_v0_8_0).
--
-- There is no data conversion in the rebuild: no database applying this
-- migration ever had an `abandoned` column. The column list is still spelled
-- out explicitly rather than `select *` — `insert ... select` is positional,
-- and a silent column-order drift here would scramble every row of the largest
-- table in the system.
--
-- Nothing has a foreign key TO `results`, so dropping it fires no cascade into
-- a live table. foreign_keys is still switched off around the rebuild because
-- `results` has keys OUT to organizations/checks/workers and legacy databases
-- can carry rows whose check was hard-deleted underneath them (the case
-- ReapAbandonedResults explicitly tolerates); re-inserting those with the
-- constraint on would fail the migration rather than carry the data across.
-- The PRAGMAs sit in their own statements so they run in autocommit — a
-- PRAGMA foreign_keys inside a transaction is silently a no-op.

PRAGMA foreign_keys=OFF;

--bun:split

create table results_new (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade, -- Owning organization
  check_uid         text not null references checks(uid) on delete cascade, -- Check that produced this result
  period_type       text not null default 'raw' check (period_type in ('raw', 'hour', 'day', 'month', 'year')), -- Granularity: raw or aggregated
  period_start      text not null, -- Execution timestamp (raw) or aggregation period start
  period_end        text, -- Aggregation period end. NULL for raw results
  region            text, -- Region where the check was executed

  -- Raw result fields (period_type = 'raw')
  worker_uid        text references workers(uid) on delete set null, -- Worker that executed this check (raw only)
  status            integer check (status in (0, 1, 2, 3, 4, 5, 6, 7, 8, 9)), -- 1=created, 2=running, 3=up, 4=down, 5=timeout, 6=error, 7=degraded (aggregated), 8=warning, 9=abandoned (reaper-minted, excluded from availability)
  duration          real, -- Total check duration in milliseconds (raw only)
  metrics           text, -- Numerical metrics: ttfb, dnsTime, tlsHandshake, etc. (raw only)
  output            text, -- Diagnostic output: error messages, HTTP status, headers (raw only)

  -- Aggregated fields (period_type = 'hour', 'day', 'month', 'year')
  total_checks      integer, -- Number of check executions in this period
  successful_checks integer, -- Number of successful executions in this period
  duration_min      real, -- Minimum duration in this period
  duration_max      real, -- Maximum duration in this period
  duration_p95      real, -- 95th percentile duration in this period
  duration_avg      real, -- Average duration in this period

  created_at        text not null default (datetime('now'))
);

--bun:split

insert into results_new (
  uid, organization_uid, check_uid, period_type, period_start, period_end, region,
  worker_uid, status, duration, metrics, output,
  total_checks, successful_checks, duration_min, duration_max, duration_p95, duration_avg,
  created_at
)
select
  uid, organization_uid, check_uid, period_type, period_start, period_end, region,
  worker_uid, status, duration, metrics, output,
  total_checks, successful_checks, duration_min, duration_max, duration_p95, duration_avg,
  created_at
from results;

--bun:split

drop table results;

--bun:split

alter table results_new rename to results;

--bun:split

create index results_raw_idx on results (organization_uid, check_uid, period_start desc) where period_type = 'raw';

--bun:split

create index results_aggregated_idx on results (organization_uid, check_uid, period_type, period_start desc) where period_type != 'raw';

--bun:split

-- NOT 001's definition: migration 006 replaced it with this NULL-proof form
-- over coalesce(region, '') to close the aggregation poison-pill loop that
-- duplicated `hour` rows unbounded (spec 2026-07-11-16). SQLite treats every
-- NULL as distinct in a unique index, so the bare `region` form silently
-- stops constraining region-less rollups at all. Copied verbatim from
-- 006_v0_5_0.up.sql — a rebuild that recreates indexes from the ORIGINAL
-- create-table migration reopens every index fix shipped since.
create unique index results_aggregated_unique_idx
  on results (organization_uid, check_uid, coalesce(region, ''), period_type, period_start)
  where period_type != 'raw';

--bun:split

-- The reaper's candidate index: status=1 (created) only, never status=2.
create index idx_results_lifecycle_pending on results (period_start)
  where period_type = 'raw' and status = 1;

--bun:split

PRAGMA foreign_keys=ON;
