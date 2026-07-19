-- solidping v0.5.0 — consolidated release migration. Three independent pieces
-- developed during this release cycle, squashed into the single migration
-- v0.5.0 ships (net final DDL — see wiki/conventions/database.md "one
-- migration per release"; none of the objects below were added then removed
-- within the cycle, so the net DDL is this straightforward sequence):
--
-- (1) Aggregation hardening (specs 2026-07-11-16 §3 and 2026-07-12-01 §3):
--     two independent, idempotent repairs of the results table.
-- (2) Deported (org-scoped) check agents over WebSocket (spec 2026-07-16-02):
--     agents + agent_enrollment_tokens tables, drops the legacy plaintext
--     workers.token column, adds region-sealed config columns.
-- (3) Org-level default escalation policy (spec 2026-07-19-01): a nullable
--     fallback pointer from an organization to an escalation policy.

-- ============================================================
-- (1) Aggregation hardening
-- ============================================================
--
-- (1a) Purge FK-orphan results rows (results.check_uid with no matching checks
--     row). Postgres enforces results.check_uid → checks.uid at the schema
--     level, so it cannot actually hold orphans and this delete is a no-op; it
--     is shipped for symmetry with the SQLite migration (sync-pg-to-sqlite),
--     where per-connection FK enforcement can be bypassed and orphaned rows do
--     occur — those orphans are a deterministic poison pill that fails the
--     aggregation rollup INSERT and permanently halts the org's aggregation.
--
-- (1b) Close the NULL-region hole in results_aggregated_unique_idx. The old
--     index on (organization_uid, check_uid, region, period_type,
--     period_start) never fired for region IS NULL rows — Postgres treats
--     NULLs as distinct — so the aggregation poison-pill loop duplicated
--     'hour' rows unbounded. Dedupe the existing non-raw rows first (keep the
--     best survivor per bucket: highest total_checks, tie-break earliest
--     created_at, then uid), then rebuild the index over coalesce(region, '').
--
-- Idempotent / no-op on a clean database. This is also how a polluted database
-- (e.g. solidping_dev) gets its orphaned and duplicate rollup rows removed on
-- deploy.

-- (1a) purge FK-orphan rows
delete from results
where check_uid not in (select uid from checks);

-- (1b) dedupe duplicate aggregated rows, then rebuild the NULL-proof unique index
delete from results
where uid in (
  select uid from (
    select uid,
      row_number() over (
        partition by organization_uid, check_uid, coalesce(region, ''), period_type, period_start
        order by coalesce(total_checks, -1) desc, created_at asc, uid asc
      ) as rn
    from results
    where period_type != 'raw'
  ) ranked
  where rn > 1
);

drop index if exists results_aggregated_unique_idx;

create unique index results_aggregated_unique_idx
  on results (organization_uid, check_uid, coalesce(region, ''), period_type, period_start)
  where period_type != 'raw';

-- ============================================================
-- (2) Deported (org-scoped) check agents over WebSocket
-- ============================================================
-- Adds the agents + agent_enrollment_tokens tables and removes the legacy
-- plaintext workers.token column (the HTTP edge-worker API and its spw_
-- bearer tokens are replaced by the WebSocket agent transport, which
-- authenticates by Ed25519 signature and never stores a usable credential —
-- only public keys).

-- Deported agents: one row per enrolled agent, hard-scoped to exactly one
-- private region (`@<org>/<region>`). The DB holds only public keys.
create table agents (
  uid                 uuid        primary key default gen_random_uuid(),
  organization_uid    uuid        not null references organizations(uid),
  region              text        not null,
  name                text        not null,
  ed25519_public_key  text        not null,
  x25519_public_key   text        not null,
  fingerprint         text        not null,
  status              text        not null default 'active',
  last_seen_at        timestamptz,
  enrolled_at         timestamptz not null default now(),
  revoked_at          timestamptz,
  created_at          timestamptz not null default now(),
  updated_at          timestamptz not null default now(),
  deleted_at          timestamptz
);

create index idx_agents_org_region on agents (organization_uid, region) where deleted_at is null;
create index idx_agents_status     on agents (status)                    where deleted_at is null;
create unique index idx_agents_ed25519 on agents (ed25519_public_key)    where deleted_at is null;

comment on table agents is 'Deported (org-scoped) check agents connecting outbound over WebSocket. Hard-scoped to one private region; DB stores only public keys.';
comment on column agents.region is 'Fully-qualified private-region slug (@<org>/<region>) the agent is bound to.';
comment on column agents.ed25519_public_key is 'Base64 Ed25519 identity public key; verifies reconnect signatures.';
comment on column agents.x25519_public_key is 'age X25519 recipient (age1...) credentials are sealed to.';

-- One-shot enrollment tokens: bind a future agent to (org, region). Only the
-- SHA-256 hash of the spe_ token is stored; single-use under concurrency.
create table agent_enrollment_tokens (
  uid                  uuid        primary key default gen_random_uuid(),
  organization_uid     uuid        not null references organizations(uid),
  region               text        not null,
  token_hash           text        not null,
  expires_at           timestamptz not null,
  used_at              timestamptz,
  used_by_agent_uid    uuid,
  created_by_user_uid  uuid,
  created_at           timestamptz not null default now(),
  deleted_at           timestamptz
);

create unique index idx_agent_enrollment_tokens_hash on agent_enrollment_tokens (token_hash) where deleted_at is null;
create index idx_agent_enrollment_tokens_org on agent_enrollment_tokens (organization_uid) where deleted_at is null;

comment on table agent_enrollment_tokens is 'One-shot agent enrollment tokens (spe_ prefix). Stores only the SHA-256 hash; consumed atomically at enrollment.';

-- Drop the legacy edge-worker bearer token. The HTTP worker API that minted and
-- matched these plaintext spw_ tokens is gone; agents authenticate by Ed25519
-- signature, so the DB holds no usable worker/agent credential at all. The
-- partial index on the column drops with it.
alter table workers drop column token;

-- Region-sealed credentials (phase 2): the age-X25519 (v2) envelope of a
-- check's secret fields, sealed to the X25519 keys of the private region's
-- active agents. Sealed-only checks (private regions only) leave
-- config_private NULL — the server cannot decrypt their secrets after write.
alter table checks     add column config_sealed text;
alter table check_jobs add column config_sealed text;

comment on column checks.config_sealed is 'Region-sealed (age X25519) envelope of secret config fields for private-region agents. NULL when the check targets no private region.';
comment on column check_jobs.config_sealed is 'Copy of checks.config_sealed shipped verbatim to deported agents; never decrypted server-side.';

-- ============================================================
-- (3) Org-level default escalation policy
-- ============================================================
-- A nullable pointer from an organization to the escalation policy that
-- applies to any of its checks that resolve to no policy of their own
-- (check → group → ORG DEFAULT → none). Opt-in: unset (NULL) reproduces
-- today's behavior exactly. `on delete set null` mirrors
-- checks.escalation_policy_uid / check_groups: when the referenced policy is
-- deleted, the pointer clears rather than blocking the delete or dangling.
alter table organizations
  add column default_escalation_policy_uid uuid
    references escalation_policies(uid) on delete set null;

comment on column organizations.default_escalation_policy_uid is
  'Org-wide fallback escalation policy for checks that resolve to no policy (check > group > org default > none). NULL = no org default (legacy behavior).';
