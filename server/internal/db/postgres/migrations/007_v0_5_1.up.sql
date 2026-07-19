-- solidping v0.5.1 — backfill patch for environments that drifted from
-- 006_v0_5_0. `006_v0_5_0.up.sql` was first committed mid-cycle with only its
-- aggregation-hardening DDL, and something ran that early version against
-- k8xp's solidping_dev database on 2026-07-12. Bun tracks migrations by
-- numeric filename prefix only (it strips the `_v0_5_0` suffix down to
-- `"006"`), so once that prefix was recorded as applied, the fuller
-- 006_v0_5_0.up.sql later shipped in the v0.5.0 tag was silently skipped on
-- every subsequent deploy: the agents/agent_enrollment_tokens tables,
-- checks.config_sealed/check_jobs.config_sealed, and
-- organizations.default_escalation_policy_uid never got created, and the
-- v0.5.0 pod crash-loops on boot (`column
-- organization.default_escalation_policy_uid does not exist`).
--
-- Deliberately NOT included: `alter table workers drop column token`. The
-- solidping-checks-us1/eu2 deployments in solidping-dev are still on v0.4.1,
-- which authenticates with the legacy workers.token bearer token; dropping
-- the column now would break them. That drop stays deferred to a future
-- release once those workers move to the WebSocket agent transport.
--
-- Every statement below is guarded (IF NOT EXISTS / idempotent), so this is
-- a safe no-op on any database where 006_v0_5_0 already ran in full.

-- ============================================================
-- Deported (org-scoped) check agents over WebSocket
-- ============================================================
create table if not exists agents (
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

create index if not exists idx_agents_org_region on agents (organization_uid, region) where deleted_at is null;
create index if not exists idx_agents_status     on agents (status)                    where deleted_at is null;
create unique index if not exists idx_agents_ed25519 on agents (ed25519_public_key)    where deleted_at is null;

comment on table agents is 'Deported (org-scoped) check agents connecting outbound over WebSocket. Hard-scoped to one private region; DB stores only public keys.';
comment on column agents.region is 'Fully-qualified private-region slug (@<org>/<region>) the agent is bound to.';
comment on column agents.ed25519_public_key is 'Base64 Ed25519 identity public key; verifies reconnect signatures.';
comment on column agents.x25519_public_key is 'age X25519 recipient (age1...) credentials are sealed to.';

create table if not exists agent_enrollment_tokens (
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

create unique index if not exists idx_agent_enrollment_tokens_hash on agent_enrollment_tokens (token_hash) where deleted_at is null;
create index if not exists idx_agent_enrollment_tokens_org on agent_enrollment_tokens (organization_uid) where deleted_at is null;

comment on table agent_enrollment_tokens is 'One-shot agent enrollment tokens (spe_ prefix). Stores only the SHA-256 hash; consumed atomically at enrollment.';

alter table checks     add column if not exists config_sealed text;
alter table check_jobs add column if not exists config_sealed text;

comment on column checks.config_sealed is 'Region-sealed (age X25519) envelope of secret config fields for private-region agents. NULL when the check targets no private region.';
comment on column check_jobs.config_sealed is 'Copy of checks.config_sealed shipped verbatim to deported agents; never decrypted server-side.';

-- ============================================================
-- Org-level default escalation policy
-- ============================================================
alter table organizations
  add column if not exists default_escalation_policy_uid uuid
    references escalation_policies(uid) on delete set null;

comment on column organizations.default_escalation_policy_uid is
  'Org-wide fallback escalation policy for checks that resolve to no policy (check > group > org default > none). NULL = no org default (legacy behavior).';
