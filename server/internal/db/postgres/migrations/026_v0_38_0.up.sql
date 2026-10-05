-- v0.38.0 — the ONE consolidated Postgres migration for the (still unreleased)
-- v0.38.0 release. 025_v0_34_0 shipped and is frozen, so this is the next free
-- number; every schema change of this cycle is appended here as a SECTION.
--
--   SECTION: results-duration-p50
--                              results.duration_p50: the median duration
--   SECTION: multistep-bulk-checks
--                              check_jobs.step_*: resumable multi-step runs on
--                              the bulk lane (lane = 2)
--   SECTION: check-versions
--                              check_versions: one row per applied, proposed
--                              or rejected definition of a check
--
-- Additive and re-runnable (nullable column, `if not exists`).

alter table results add column if not exists duration_p50 real;

--bun:split

comment on column results.duration_p50 is
  'Median (nearest-rank p50) duration in milliseconds of the probes this row summarizes. NULL on rows that predate the column or carry no measured duration; never backfilled, and distinct from a real 0.';

--bun:split

-- SECTION: multistep-bulk-checks (spec 2026-10-03-03)
-- A multi-step check (the website crawl) runs as a series of short slices on
-- a third claim lane, bulk (lane = 2). These columns point at the run in
-- progress; its state lives in a `checks/<uid>/step-state` attachment. All
-- NULL / 0 when no run is in progress. Additive and re-runnable.

alter table check_jobs add column if not exists step_run_uid text;
alter table check_jobs add column if not exists step_run_started_at timestamptz;
alter table check_jobs add column if not exists step_count int not null default 0;
alter table check_jobs add column if not exists step_state_file_uid text;
alter table check_jobs add column if not exists step_failures int not null default 0;

--bun:split

create index if not exists idx_check_jobs_claim_bulk
    on check_jobs (effective_scheduled_at) where lane = 2;

--bun:split

comment on column check_jobs.step_run_uid is
  'UUID of the multi-step run in progress (spec 2026-10-03-03), NULL when idle. Slice writes are fenced on it.';
comment on column check_jobs.step_run_started_at is
  'Start of the multi-step run in progress: the maxRunDuration deadline and the next-period anchor.';
comment on column check_jobs.step_count is
  'Slices completed by the multi-step run in progress.';
comment on column check_jobs.step_state_file_uid is
  'files.uid of the CURRENT step-state attachment; NULL on the first slice. The two newest state files are kept.';
comment on column check_jobs.step_failures is
  'Consecutive failed slices of the run in progress; three end the run with an error result.';

--bun:split

-- SECTION: check-versions (spec 2026-10-03-06)
create table if not exists check_versions (
  uid                  uuid primary key,
  organization_uid     uuid not null references organizations(uid) on delete cascade,
  check_uid            uuid not null references checks(uid) on delete cascade,
  version              integer not null,
  snapshot             jsonb not null,
  snapshot_hash        text not null,
  status               text not null default 'applied'
                         check (status in ('applied', 'proposed', 'rejected')),
  base_version         integer,
  origin               text not null default 'system'
                         check (origin in ('user', 'api', 'apply', 'mcp', 'system', 'ai_generate', 'ai_repair')),
  actor_user_uid       text,
  reason               text,
  decided_by_user_uid  text,
  decided_at           timestamptz,
  created_at           timestamptz not null default now(),
  updated_at           timestamptz not null default now()
);

--bun:split

create unique index if not exists check_versions_check_version_idx
  on check_versions (check_uid, version desc);

--bun:split

create index if not exists idx_check_versions_proposed
  on check_versions (check_uid) where status = 'proposed';

--bun:split

create index if not exists idx_check_versions_organization
  on check_versions (organization_uid);

--bun:split

comment on table check_versions is
  'Version history of a check definition (name, slug, description, type, public config, group, placement, quorum, enabled, period, labels). Never holds secrets. Spec 2026-10-03-06.';

--bun:split

comment on column check_versions.organization_uid is 'Organization owning the check.';

--bun:split

comment on column check_versions.check_uid is 'The versioned check.';

--bun:split

comment on column check_versions.version is 'Per-check version number, starting at 1, unique with check_uid. Proposals take a number too.';

--bun:split

comment on column check_versions.snapshot is 'The check definition as JSON. Secret config keys (SecretFields, export-redacted fields, config_private_keys) are never included.';

--bun:split

comment on column check_versions.snapshot_hash is 'Hex SHA-256 of the canonical snapshot JSON. A write whose snapshot hashes like the latest applied version records nothing.';

--bun:split

comment on column check_versions.status is 'applied (was or is the live definition), proposed (awaiting approval), rejected (a refused proposal).';

--bun:split

comment on column check_versions.base_version is 'For a proposal: the applied version it was built on. Approving it answers 409 when that is no longer the latest applied version. NULL otherwise.';

--bun:split

comment on column check_versions.origin is 'Where the change came from: user (dashboard), api (API token), apply (config-as-code apply/import), mcp, system (no caller), ai_generate, ai_repair.';

--bun:split

comment on column check_versions.actor_user_uid is 'User who made the change, NULL for system changes. Deliberately not a foreign key: like checks.created_by, it is a historical attribution that outlives the account.';

--bun:split

comment on column check_versions.reason is 'Optional one-line note (e.g. "restored v3"). NULL when none.';

--bun:split

comment on column check_versions.decided_by_user_uid is 'User who approved or rejected a proposal. NULL when undecided or not a proposal.';

--bun:split

comment on column check_versions.decided_at is 'When a proposal was approved or rejected. NULL when undecided or not a proposal.';
