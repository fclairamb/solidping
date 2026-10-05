-- v0.38.0 — SQLite twin of the Postgres 026_v0_38_0 migration.
--
-- SECTION: results-duration-p50
-- SQLite has no `add column if not exists`; this runs once on a database that
-- has never seen 026. The column is nullable, so the add is metadata-only.

alter table results add column duration_p50 real; -- Median (nearest-rank p50) duration in ms. NULL on rows that predate the column or carry no measured duration

--bun:split

-- SECTION: multistep-bulk-checks (spec 2026-10-03-03)
-- Pointers to the multi-step run in progress (the crawl) on the bulk lane
-- (lane = 2). All NULL / 0 when idle.

alter table check_jobs add column step_run_uid text; -- uuid of the run in progress, NULL when idle
--bun:split
alter table check_jobs add column step_run_started_at text; -- start of the run: deadline and next-period anchor
--bun:split
alter table check_jobs add column step_count integer not null default 0; -- slices completed by the run
--bun:split
alter table check_jobs add column step_state_file_uid text; -- files.uid of the CURRENT state, NULL on the first slice
--bun:split
alter table check_jobs add column step_failures integer not null default 0; -- consecutive failed slices

--bun:split

create index if not exists idx_check_jobs_claim_bulk
    on check_jobs (effective_scheduled_at) where lane = 2;

--bun:split

-- SECTION: check-versions (spec 2026-10-03-06)
-- check_versions: version history of a check definition. Never holds secrets.
create table if not exists check_versions (
  uid                  text primary key,
  organization_uid     text not null references organizations(uid) on delete cascade, -- Organization owning the check
  check_uid            text not null references checks(uid) on delete cascade, -- The versioned check
  version              integer not null, -- Per-check version number, starting at 1
  snapshot             text not null, -- The check definition as JSON, without secrets
  snapshot_hash        text not null, -- Hex SHA-256 of the canonical snapshot JSON
  status               text not null default 'applied'
                         check (status in ('applied', 'proposed', 'rejected')), -- applied | proposed | rejected
  base_version         integer, -- Applied version a proposal was built on, NULL otherwise
  origin               text not null default 'system'
                         check (origin in ('user', 'api', 'apply', 'mcp', 'system', 'ai_generate', 'ai_repair')), -- Where the change came from
  actor_user_uid       text, -- User who made the change, NULL for system
  reason               text, -- Optional one-line note
  decided_by_user_uid  text, -- User who approved or rejected a proposal
  decided_at           text, -- When a proposal was approved or rejected
  created_at           text not null default (datetime('now')),
  updated_at           text not null default (datetime('now'))
);

--bun:split

create unique index if not exists check_versions_check_version_idx
  on check_versions (check_uid, version desc);

--bun:split

create index if not exists idx_check_versions_check_status
  on check_versions (check_uid, status);

--bun:split

create index if not exists idx_check_versions_organization
  on check_versions (organization_uid);
