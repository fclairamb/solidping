-- solidping v0.5.0 — consolidated release migration. SQLite mirror of the
-- Postgres 006 migration (sync-pg-to-sqlite). Three independent pieces
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
--     row). SQLite foreign keys are per-connection: any connection re-opened by
--     database/sql after an error (or an external sqlite3 CLI session) runs with
--     FKs OFF, so a hard delete of a check could skip the ON DELETE CASCADE on
--     results.check_uid and leave orphaned rows behind. Those orphans are a
--     deterministic poison pill — the aggregation rollup INSERT fails the
--     results.check_uid → checks.uid foreign key (SQLite error 787) and
--     permanently halts the org's aggregation. Soft-deleted checks still have a
--     row and are preserved. On Postgres this delete is a no-op by FK
--     construction.
--
-- (1b) Close the NULL-region hole in results_aggregated_unique_idx. The old
--     index on (organization_uid, check_uid, region, period_type,
--     period_start) never fired for region IS NULL rows — SQLite also treats
--     NULLs as distinct in unique indexes — so the aggregation poison-pill
--     loop duplicated 'hour' rows unbounded. Dedupe the existing non-raw rows
--     first (keep the best survivor per bucket: highest total_checks,
--     tie-break earliest created_at, then uid), then rebuild the index over
--     coalesce(region, '').
--
-- Idempotent / no-op on a clean database.

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
-- SQLite mirror of the Postgres migration. Adds the agents +
-- agent_enrollment_tokens tables and removes the legacy plaintext
-- workers.token column (replaced by WebSocket agent transport with Ed25519
-- signature auth).

create table agents (
  uid                 text primary key,
  organization_uid    text not null,
  region              text not null,
  name                text not null,
  ed25519_public_key  text not null,
  x25519_public_key   text not null,
  fingerprint         text not null,
  status              text not null default 'active',
  last_seen_at        text,
  enrolled_at         text not null default (datetime('now')),
  revoked_at          text,
  created_at          text not null default (datetime('now')),
  updated_at          text not null default (datetime('now')),
  deleted_at          text
);

create index idx_agents_org_region on agents (organization_uid, region) where deleted_at is null;
create index idx_agents_status     on agents (status)                    where deleted_at is null;
create unique index idx_agents_ed25519 on agents (ed25519_public_key)    where deleted_at is null;

create table agent_enrollment_tokens (
  uid                  text primary key,
  organization_uid     text not null,
  region               text not null,
  token_hash           text not null,
  expires_at           text not null,
  used_at              text,
  used_by_agent_uid    text,
  created_by_user_uid  text,
  created_at           text not null default (datetime('now')),
  deleted_at           text
);

create unique index idx_agent_enrollment_tokens_hash on agent_enrollment_tokens (token_hash) where deleted_at is null;
create index idx_agent_enrollment_tokens_org on agent_enrollment_tokens (organization_uid) where deleted_at is null;

-- Drop the legacy edge-worker bearer token (see the Postgres mirror). SQLite
-- refuses to drop an indexed column, so its index goes first.
drop index if exists idx_workers_token;
alter table workers drop column token;

-- Region-sealed credentials (phase 2): the age-X25519 (v2) envelope of a
-- check's secret fields, sealed to the X25519 keys of the private region's
-- active agents. Sealed-only checks (private regions only) leave
-- config_private NULL — the server cannot decrypt their secrets after write.
alter table checks add column config_sealed text;
alter table check_jobs add column config_sealed text;

-- ============================================================
-- (3) Org-level default escalation policy
-- ============================================================
-- SQLite mirror of the Postgres migration. A nullable pointer from an
-- organization to the escalation policy applied to any of its checks that
-- resolve to no policy of their own (check > group > ORG DEFAULT > none).
-- Opt-in: unset (NULL) reproduces today's behavior exactly. `on delete set
-- null` mirrors checks.escalation_policy_uid / check_groups. SQLite permits a
-- REFERENCES clause on ADD COLUMN because the added column defaults to NULL.
alter table organizations
  add column default_escalation_policy_uid text
    references escalation_policies(uid) on delete set null;
