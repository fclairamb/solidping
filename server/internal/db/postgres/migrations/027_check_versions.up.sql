-- Scratch migration for the v0.39.0 cycle (spec 2026-10-03-06): check version
-- history. 026_v0_38_0 is in its release PR, so this lands in a new file; it
-- is folded into the cycle's consolidated NNN_vX_Y_Z file at release time.
--
--   SECTION: check-versions
--                              check_versions: one row per applied, proposed
--                              or rejected definition of a check
--
-- Additive and re-runnable (`if not exists`).

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
  actor_user_uid       uuid references users(uid) on delete set null,
  reason               text,
  decided_by_user_uid  uuid references users(uid) on delete set null,
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

comment on column check_versions.actor_user_uid is 'User who made the change. NULL for system changes or when the user was deleted.';

--bun:split

comment on column check_versions.reason is 'Optional one-line note (e.g. "restored v3"). NULL when none.';

--bun:split

comment on column check_versions.decided_by_user_uid is 'User who approved or rejected a proposal. NULL when undecided or not a proposal.';

--bun:split

comment on column check_versions.decided_at is 'When a proposal was approved or rejected. NULL when undecided or not a proposal.';
