-- v0.41.0 — SQLite twin of the Postgres 030_v0_41_0 migration.
--
-- Frozen once tagged, like every migration: a later change goes in 031.

-- SECTION: user-preferences (spec 2026-10-21-01)
-- users.preferences: the language and timezone a user's emails are written in,
-- as JSON {"locale": "en|fr|de|es", "timezone": "<IANA zone>"}; NULL = never
-- set (English, UTC). Validated in Go, no CHECK.
alter table users add column preferences text;

-- SECTION: discovered-checks-region (spec 2026-10-21-01)
-- discovered_checks.region: region of the scan that found the row; '' = the
-- server itself, '@slug' = a private region. NOT NULL (NULLs are distinct in
-- unique indexes) and part of the upsert identity.
alter table discovered_checks add column region text not null default '';

drop index if exists idx_discovered_checks_identity_active;

create unique index idx_discovered_checks_identity_active
  on discovered_checks (organization_uid, source, group_key, slug, region)
  where deleted_at is null and promoted_to_check_uid is null;

-- SECTION: worker-tasks (spec 2026-10-22-02)
-- worker_tasks: units of work a worker (cloud or agent) claims under a lease
-- and may run in several slices, the state between slices held by the server.
create table if not exists worker_tasks (
  uid              text primary key,
  organization_uid text not null references organizations(uid) on delete cascade, -- Owning organization; an agent only claims its own org's tasks
  kind             text not null, -- Task kind (crawl, discovery...)
  region           text not null, -- Region matched exactly by the claim: cloud slug or @slug
  check_uid        text references checks(uid) on delete cascade, -- Parent check; NULL when the parent is a job
  job_uid          text references jobs(uid) on delete cascade, -- Parent job; NULL when the parent is a check
  payload          text, -- Task input as JSON; NULL when none
  progress         text, -- Small progress summary as JSON, shown in the UI
  status           text not null default 'pending'
                     check (status in ('pending', 'claimed', 'done', 'failed', 'cancelled')), -- pending | claimed | done | failed | cancelled
  worker_uid       text, -- Worker or agent holding the lease; NULL when not claimed
  lease_until      text, -- End of the current lease; NULL when not claimed
  attempts         integer not null default 0, -- Claims of the current slice; the 4th fails the task
  slice_count      integer not null default 0, -- Slices completed
  slice_failures   integer not null default 0, -- Consecutive failed slices; 3 fail the task
  state_file_uid   text, -- File holding the next slice's state; NULL before the first slice
  not_before       text not null, -- Not claimed before this time
  run_started_at   text, -- When the first slice was claimed
  error_code       text, -- Machine code of a failed task
  created_at       text not null default (datetime('now')),
  updated_at       text not null default (datetime('now')),
  check ((check_uid is null) <> (job_uid is null))
);

create index if not exists idx_worker_tasks_claim
  on worker_tasks (region, kind, status, not_before);

create index if not exists idx_worker_tasks_org_region_status
  on worker_tasks (organization_uid, region, status);

create index if not exists idx_worker_tasks_job
  on worker_tasks (job_uid);

create unique index if not exists worker_tasks_check_active_idx
  on worker_tasks (check_uid) where status in ('pending', 'claimed');
