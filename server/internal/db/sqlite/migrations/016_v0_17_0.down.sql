-- Parity only: rebuild `results` with the `abandoned` flag back and the
-- narrower `status in (0..8)` domain, folding status 9 back into
-- "error + abandoned" on the way across. Same *_new rebuild and same
-- foreign_keys handling as the up migration.

PRAGMA foreign_keys=OFF;

--bun:split

create table results_new (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade,
  check_uid         text not null references checks(uid) on delete cascade,
  period_type       text not null default 'raw' check (period_type in ('raw', 'hour', 'day', 'month', 'year')),
  period_start      text not null,
  period_end        text,
  region            text,

  worker_uid        text references workers(uid) on delete set null,
  status            integer check (status in (0, 1, 2, 3, 4, 5, 6, 7, 8)),
  duration          real,
  metrics           text,
  output            text,

  total_checks      integer,
  successful_checks integer,
  duration_min      real,
  duration_max      real,
  duration_p95      real,
  duration_avg      real,

  created_at        text not null default (datetime('now')),

  abandoned         integer not null default 0 -- True (1) when the abandoned-result reaper finalized this row from a stale created marker.
);

--bun:split

insert into results_new (
  uid, organization_uid, check_uid, period_type, period_start, period_end, region,
  worker_uid, status, duration, metrics, output,
  total_checks, successful_checks, duration_min, duration_max, duration_p95, duration_avg,
  created_at, abandoned
)
select
  uid, organization_uid, check_uid, period_type, period_start, period_end, region,
  worker_uid,
  case when period_type = 'raw' and status = 9 then 6 else status end,
  duration, metrics, output,
  total_checks, successful_checks, duration_min, duration_max, duration_p95, duration_avg,
  created_at,
  case when period_type = 'raw' and status = 9 then 1 else 0 end
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

create index idx_results_lifecycle_pending on results (period_start)
  where period_type = 'raw' and status = 1;

--bun:split

PRAGMA foreign_keys=ON;
