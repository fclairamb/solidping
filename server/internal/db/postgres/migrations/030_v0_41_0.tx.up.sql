-- v0.41.0 — Postgres migration.
--
-- Frozen once tagged, like every migration: a later change goes in 031.
--
--   SECTION: user-preferences   users.preferences
--   SECTION: worker-tasks       worker_tasks
--   SECTION: crawl-tasks        worker_tasks.units_granted

-- ==========================================================================
-- SECTION: user-preferences (spec 2026-10-21-01)
-- ==========================================================================
-- The language and timezone a user's emails are written in. JSON so a new
-- preference needs no migration. No CHECK on purpose: the supported languages
-- and the tz database are validated in Go (internal/i18n), so adding a fifth
-- language is not a two-engine migration.
alter table users add column if not exists preferences jsonb;

--bun:split

comment on column users.preferences is 'User preferences the server follows when writing to the user: {"locale": "en|fr|de|es", "timezone": "<IANA zone>"}, each key optional. NULL when nothing was ever set (emails then fall back to English and UTC).';

--bun:split

-- ==========================================================================
-- SECTION: discovered-checks-region (spec 2026-10-21-01)
-- ==========================================================================
-- The region of the scan that found the row: '' = the server itself, '@slug'
-- = a private region's agent. NOT NULL on purpose: NULLs are distinct in
-- unique indexes, so server-side re-scans would duplicate rows.
alter table discovered_checks add column if not exists region text not null default '';

--bun:split

comment on column discovered_checks.region is 'Region of the scan that found the row: empty = the server itself, @slug = a private region. Part of the upsert identity.';

--bun:split

drop index if exists idx_discovered_checks_identity_active;

--bun:split

create unique index idx_discovered_checks_identity_active
  on discovered_checks (organization_uid, source, group_key, slug, region)
  where deleted_at is null and promoted_to_check_uid is null;

--bun:split

-- ==========================================================================
-- SECTION: worker-tasks (spec 2026-10-22-02)
-- ==========================================================================
-- One unit of work a worker claims, holds under a lease, may run in several
-- slices with server-held state in between, and reports back. Every kind
-- (crawl, discovery chunks...) shares this table, its lease and its fencing.
create table if not exists worker_tasks (
  uid              uuid primary key,
  organization_uid uuid not null references organizations(uid) on delete cascade,
  kind             text not null,
  region           text not null,
  check_uid        uuid references checks(uid) on delete cascade,
  job_uid          uuid references jobs(uid) on delete cascade,
  payload          jsonb,
  progress         jsonb,
  status           text not null default 'pending'
                     check (status in ('pending', 'claimed', 'done', 'failed', 'cancelled')),
  worker_uid       text,
  lease_until      timestamptz,
  attempts         integer not null default 0,
  slice_count      integer not null default 0,
  slice_failures   integer not null default 0,
  state_file_uid   text,
  not_before       timestamptz not null,
  run_started_at   timestamptz,
  error_code       text,
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now(),
  constraint worker_tasks_one_parent check ((check_uid is null) <> (job_uid is null))
);

--bun:split

create index if not exists idx_worker_tasks_claim
  on worker_tasks (region, kind, status, not_before);

--bun:split

create index if not exists idx_worker_tasks_org_region_status
  on worker_tasks (organization_uid, region, status);

--bun:split

create index if not exists idx_worker_tasks_job
  on worker_tasks (job_uid);

--bun:split

create unique index if not exists worker_tasks_check_active_idx
  on worker_tasks (check_uid) where status in ('pending', 'claimed');

--bun:split

comment on table worker_tasks is 'Units of work a worker (cloud or agent) claims under a lease and may run in several slices, the state between slices being held by the server.';

--bun:split

comment on column worker_tasks.organization_uid is 'Organization the task belongs to. An agent only claims the tasks of its own organization.';

--bun:split

comment on column worker_tasks.kind is 'Task kind, matched against the kinds registered on the server and run by the worker (e.g. crawl, discovery).';

--bun:split

comment on column worker_tasks.region is 'Region the task must run in, matched exactly by the claim: a cloud region slug, or @slug for a private region.';

--bun:split

comment on column worker_tasks.check_uid is 'Parent check of the task. NULL when the parent is a job. Exactly one of check_uid and job_uid is set.';

--bun:split

comment on column worker_tasks.job_uid is 'Parent job of the task, cleaned with it. NULL when the parent is a check. Exactly one of check_uid and job_uid is set.';

--bun:split

comment on column worker_tasks.payload is 'Input of the task, as the kind defines it. NULL when the kind needs none.';

--bun:split

comment on column worker_tasks.progress is 'Small progress summary shown in the UI, sent by the worker with each slice. NULL before the first slice.';

--bun:split

comment on column worker_tasks.status is 'pending (waiting for a worker), claimed (leased by worker_uid), done, failed, cancelled. A pending row untouched for 24 h is read as failed.';

--bun:split

comment on column worker_tasks.worker_uid is 'Worker (cloud worker uid or agent uid) holding the lease. NULL when not claimed.';

--bun:split

comment on column worker_tasks.lease_until is 'End of the current lease. NULL when not claimed. A claimed row past it can be claimed again.';

--bun:split

comment on column worker_tasks.attempts is 'Claims of the current slice. A claim that would be the 4th fails the task with max_attempts.';

--bun:split

comment on column worker_tasks.slice_count is 'Slices completed so far.';

--bun:split

comment on column worker_tasks.slice_failures is 'Consecutive failed slices. Three in a row fail the task.';

--bun:split

comment on column worker_tasks.state_file_uid is 'File holding the state for the next slice (tasks/<uid>/step-state). NULL before the first slice.';

--bun:split

comment on column worker_tasks.not_before is 'The task is not claimed before this time (retry delay, deferral).';

--bun:split

comment on column worker_tasks.run_started_at is 'When the first slice was claimed. NULL before.';

--bun:split

comment on column worker_tasks.error_code is 'Machine code of a failed task (max_attempts, slice_failures, a kind code). NULL otherwise.';

--bun:split

-- ==========================================================================
-- SECTION: crawl-tasks (spec 2026-10-22-03)
-- ==========================================================================
-- The website crawl runs on worker_tasks. A slice's units are reserved from
-- the org's checks-per-minute bucket at the claim; the grant is kept here so
-- the server refunds what the slice did not use from its own number.
alter table worker_tasks add column if not exists units_granted integer not null default 0;

--bun:split

comment on column worker_tasks.units_granted is 'Work units reserved from the org rate bucket for the slice in progress, refunded server-side when the slice reports. 0 when nothing is reserved.';
