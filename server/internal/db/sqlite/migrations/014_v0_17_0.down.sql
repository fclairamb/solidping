-- Teardown/parity half of the consolidated v0.17.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of 014_v0_17_0.up.sql,
-- so each one unwinds on a schema that still has everything the sections above
-- it created (on SQLite in particular, slo-reporting drops its `results`
-- columns before results-status-domain rebuilds the table without them).
--
-- Several sections are lossy on the way down; each says so in its own note.

-- ==========================================================================
-- SECTION: generic-attachments
-- Was scratch migration 020_generic_attachments (spec 2026-08-21-01). Teardown half.
-- ==========================================================================

-- Teardown/parity only — never run in production. SQLite mirror of the
-- generic-attachments section of postgres/migrations/014_v0_17_0.down.sql, and
-- the FIRST section here because the down file unwinds in reverse order.
--
-- LOSSY the same way the Postgres half is: dropping the columns destroys every
-- attachment link while leaving the blobs and their `files` rows behind.

drop index if exists files_org_topic_idx;

--bun:split

alter table files drop column details;

--bun:split

alter table files drop column topic;

--bun:split

-- ==========================================================================
-- SECTION: dangling-notification-routes
-- Was scratch migration 019_dangling_notification_routes (spec 2026-08-20-02). Teardown half.
-- ==========================================================================

-- Deliberately a no-op, and the FIRST section here because the down file
-- unwinds in reverse order.
--
-- The up half is a pure data cleanup: it deletes notification-route rows whose
-- contact no longer exists. There is nothing to restore — the rows carried no
-- information beyond "this dead contact once had a route", they were never
-- reachable through the API, and re-creating them would put the ghost rows
-- back on every affected user's notifications page. Down migrations undo
-- SCHEMA, not garbage collection.

select 1;

--bun:split

-- ==========================================================================
-- SECTION: slo-reporting
-- Was scratch migration 018_slo_reporting (spec 2026-08-20-01). Teardown half.
-- ==========================================================================

-- Teardown/parity only — never run in production. Reverses
-- the slo-reporting section of 014_v0_17_0.up.sql.
--
-- SQLite gained DROP COLUMN in 3.35; the results columns are dropped
-- individually, newest first.

drop table if exists report_schedules;

--bun:split

drop table if exists slos;

--bun:split

alter table results drop column maintenance_successful_checks;

--bun:split

alter table results drop column maintenance_checks;

--bun:split

alter table results drop column maintenance;

--bun:split

-- ==========================================================================
-- SECTION: incident-publications
-- Was scratch migration 017_incident_publications (spec 2026-08-19-08). Teardown half.
-- ==========================================================================

-- Teardown/parity only — never run in production. Reverses
-- the incident-publications section of 014_v0_17_0.up.sql. Same *_new rebuild technique as the up
-- migration, restoring the original status_updates shape.

PRAGMA foreign_keys=OFF;

--bun:split

-- Rows created by the auto-publish pipeline have no author and cannot survive
-- a NOT NULL author_uid.
delete from status_updates where author_uid is null;

--bun:split

create table status_updates_old (
  uid              text primary key,
  organization_uid text not null references organizations(uid),
  status_page_uid  text not null references status_pages(uid),
  section_uid      text references status_page_sections(uid),
  check_uid        text references checks(uid),
  incident_uid     text references incidents(uid),
  title            text not null,
  body_markdown    text not null,
  link_url         text,
  kind             text not null check (kind in ('investigating', 'identified', 'monitoring', 'resolved', 'maintenance', 'info')),
  published_at     text not null default (datetime('now')),
  author_uid       text not null references users(uid),
  created_at       text not null default (datetime('now')),
  updated_at       text not null default (datetime('now')),
  deleted_at       text
);

insert into status_updates_old (
  uid, organization_uid, status_page_uid, section_uid, check_uid, incident_uid,
  title, body_markdown, link_url, kind, published_at, author_uid, created_at,
  updated_at, deleted_at
)
select
  uid, organization_uid, status_page_uid, section_uid, check_uid, incident_uid,
  title, body_markdown, link_url, kind, published_at, author_uid, created_at,
  updated_at, deleted_at
from status_updates;

drop table status_updates;
alter table status_updates_old rename to status_updates;

create index idx_status_updates_org_page_pub on status_updates (organization_uid, status_page_uid, published_at desc);
create index idx_status_updates_incident on status_updates (incident_uid);
create index idx_status_updates_check on status_updates (check_uid);

--bun:split

PRAGMA foreign_keys=ON;

--bun:split

-- status_pages / status_page_resources: SQLite can DROP COLUMN for plain
-- columns that carry no unique constraint, which is the case for all four.
alter table status_page_resources drop column auto_publish;

--bun:split

alter table status_pages drop column auto_resolve;

--bun:split

alter table status_pages drop column auto_publish_delay_seconds;

--bun:split

alter table status_pages drop column auto_publish;

--bun:split

drop table if exists incident_publications;

--bun:split

-- ==========================================================================
-- SECTION: worker-version
-- Was scratch migration 016_worker_version (spec 2026-08-19-07). Teardown half.
-- ==========================================================================

-- Teardown/parity only — never run in production. Reverses
-- the worker-version section of 014_v0_17_0.up.sql.
-- SQLite mirror of the worker-version section of
-- postgres/migrations/014_v0_17_0.down.sql.

alter table workers drop column version;

--bun:split

-- ==========================================================================
-- SECTION: remove-opsgenie-integrations
-- Was scratch migration 015_remove_opsgenie_integrations (spec 2026-08-19-02). Teardown half.
-- ==========================================================================

-- Teardown/parity only — never run in production. Reverses
-- the remove-opsgenie-integrations section of 014_v0_17_0.up.sql.
--
-- There is no schema to revert: the up migration only deleted data. Deleted
-- rows cannot be un-deleted, so this is intentionally a no-op — the same
-- spirit as every other hard-delete migration in this repo (pre-1.0, no
-- tombstones). A database that ran the up migration and then this down
-- migration is NOT restored to its prior state; it is simply marked as
-- having reversed the migration.

select 1;

--bun:split

-- ==========================================================================
-- SECTION: results-status-domain
-- The v0.17.0 file's original body (specs 2026-08-18-03 and 2026-08-18-10). Teardown half.
-- ==========================================================================

-- Parity only: rebuild `results` with the narrower `status in (0..8)` domain
-- 013 left behind, and drop the reaper's index. Any row the reaper minted is
-- folded back to plain `error` on the way across — the narrower domain cannot
-- hold 9, and `error` is the closest pre-9 encoding — so the distinction this
-- release introduced is simply lost going down. Same *_new rebuild and same
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
  worker_uid,
  case when period_type = 'raw' and status = 9 then 6 else status end,
  duration, metrics, output,
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

-- idx_results_lifecycle_pending is deliberately NOT recreated: it arrived with
-- this migration, so the post-013 state does not have it. The rebuild drops it
-- along with the old table.

PRAGMA foreign_keys=ON;
