-- Scratch migration for the v0.39.0 cycle (spec 2026-10-03-06): check version
-- history. See the Postgres twin. Folded into the cycle's consolidated file at
-- release time.

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
