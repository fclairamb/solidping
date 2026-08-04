-- solidping v0.7.0 — see the postgres twin for the full rationale.
--
-- Multiple features share this one release migration; each contributes a
-- clearly separated block below. Append new blocks at the end.

-- ---------------------------------------------------------------------------
-- Custom domains for status pages (spec 2026-07-22-01)
-- ---------------------------------------------------------------------------
-- One customer-owned hostname per status page, DNS-verified with a single CNAME.
-- The GLOBAL partial unique index is the ownership-race anchor (unique
-- violation on write -> 409 CONFLICT), mirroring status_pages_org_slug_idx.
-- Timestamps are text (the cross-database convention used across these
-- migrations); failures is a plain integer counter.

alter table status_pages add column custom_domain text; -- Customer hostname (punycode/ASCII, lowercased). NULL = none.
alter table status_pages add column custom_domain_token text; -- DNS-label-safe token (lowercase base32); in token mode it is the leading label of the expected CNAME target.
alter table status_pages add column custom_domain_verified_at text; -- Last passed CNAME verification. NULL = unverified.
alter table status_pages add column custom_domain_checked_at text; -- Last periodic re-verification check.
alter table status_pages add column custom_domain_failures integer not null default 0; -- Consecutive re-verify failures; 3 clears verification.

create unique index status_pages_custom_domain_idx
  on status_pages (custom_domain)
  where custom_domain is not null and deleted_at is null;


-- ---------------------------------------------------------------------------
-- Phone (SMS/voice) contact verification (spec 2026-07-22-02)
-- ---------------------------------------------------------------------------
-- See the postgres twin for rationale. Timestamps are text (cross-database
-- convention used across these migrations); verify_attempts is a plain counter.
alter table user_contacts add column verify_code_hash text; -- SHA-256 hex of the pending 6-digit code; NULL when none pending.
alter table user_contacts add column verify_expires_at text; -- Pending code expiry (issue + 10 min).
alter table user_contacts add column verify_attempts integer not null default 0; -- Failed confirm attempts; 5 invalidates.

-- ---------------------------------------------------------------------------
-- Monthly SMS/voice usage counters (spec 2026-07-22-02)
-- ---------------------------------------------------------------------------
-- See the postgres twin. period_start is text here (cross-database convention);
-- the reserve-then-send conditional upsert keeps a burst within the cap.
create table org_usage_counters (
  organization_uid text not null,
  kind text not null,
  period_start text not null,
  count integer not null default 0,
  primary key (organization_uid, kind, period_start)
);

-- ---------------------------------------------------------------------------
-- Results table tier-1 storage trim (spec 2026-07-24-02)
-- ---------------------------------------------------------------------------
-- See the postgres twin for the full rationale. SQLite >=3.35 (the bundled
-- driver is well past that) supports ALTER TABLE ... DROP COLUMN. The partial
-- index idx_results_last_for_status references last_for_status in its WHERE
-- clause, so it must be dropped before the column or SQLite rejects the DROP
-- COLUMN. availability_pct is in no index and drops directly.

drop index if exists idx_results_last_for_status;
alter table results drop column last_for_status;
alter table results drop column availability_pct;

-- ---------------------------------------------------------------------------
-- In-server ACME/TLS asset storage (spec 2026-07-26-01)
-- ---------------------------------------------------------------------------
-- certmagic key-value store for TLS assets. Values are opaque bytes (blob);
-- timestamps are text, the cross-database convention used across these
-- migrations. Prefix scans (List/Delete) use LIKE 'prefix%', which SQLite can
-- serve from the primary-key index directly.
--
-- SECURITY: this table contains PRIVATE KEYS. Never expose it through any API,
-- export, or debug surface — only internal/tlsedge reads it.

create table tls_storage (
  key text primary key, -- certmagic storage key: slash-separated path, no leading/trailing slash.
  value blob not null, -- Opaque asset bytes as written by certmagic (PEM, JSON, DER, ...).
  modified_at text not null -- Last write time; surfaced as certmagic KeyInfo.Modified.
);

-- Expiring distributed locks backing certmagic's Locker. A stale row
-- (expires_at in the past) may be taken over by any node.
create table tls_storage_locks (
  key text primary key,
  owner text not null, -- Opaque per-process identity; Unlock only releases a lock this process still owns.
  expires_at text not null
);

create index tls_storage_locks_expires_idx on tls_storage_locks (expires_at);

-- ---------------------------------------------------------------------------
-- Platform-operated "system agents" for cloud regions (spec 2026-07-27-01)
-- ---------------------------------------------------------------------------
-- See the postgres twin for the full rationale. SQLite has no ALTER COLUMN, so
-- making organization_uid nullable (it is NULL exactly for kind = 'system')
-- means rebuilding both agent tables with the established *_new pattern.

create table agents_new (
  uid                 text primary key,
  organization_uid    text,          -- NULL exactly when kind = 'system'
  kind                text not null default 'org',
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
  deleted_at          text,
  check (kind in ('org', 'system')),
  check ((kind = 'org') = (organization_uid is not null))
);

insert into agents_new (
  uid, organization_uid, kind, region, name, ed25519_public_key, x25519_public_key,
  fingerprint, status, last_seen_at, enrolled_at, revoked_at, created_at, updated_at, deleted_at
)
select
  uid, organization_uid, 'org', region, name, ed25519_public_key, x25519_public_key,
  fingerprint, status, last_seen_at, enrolled_at, revoked_at, created_at, updated_at, deleted_at
from agents;

drop table agents;
alter table agents_new rename to agents;

create index idx_agents_org_region on agents (organization_uid, region) where deleted_at is null;
create index idx_agents_status     on agents (status)                    where deleted_at is null;
create unique index idx_agents_ed25519 on agents (ed25519_public_key)    where deleted_at is null;
create index idx_agents_kind_region on agents (kind, region)             where deleted_at is null;

-- System enrollment tokens are multi-use (max_uses NULL = unlimited) and
-- revocable; org tokens stay strictly one-shot and never set max_uses.
create table agent_enrollment_tokens_new (
  uid                  text primary key,
  organization_uid     text,          -- NULL exactly when kind = 'system'
  kind                 text not null default 'org',
  region               text not null,
  token_hash           text not null,
  expires_at           text not null,
  max_uses             integer,
  use_count            integer not null default 0,
  used_at              text,
  used_by_agent_uid    text,
  created_by_user_uid  text,
  created_at           text not null default (datetime('now')),
  deleted_at           text,
  check (kind in ('org', 'system')),
  check ((kind = 'org') = (organization_uid is not null)),
  check (kind = 'system' or max_uses is null)
);

insert into agent_enrollment_tokens_new (
  uid, organization_uid, kind, region, token_hash, expires_at, use_count,
  used_at, used_by_agent_uid, created_by_user_uid, created_at, deleted_at
)
select
  uid, organization_uid, 'org', region, token_hash, expires_at,
  case when used_at is null then 0 else 1 end,
  used_at, used_by_agent_uid, created_by_user_uid, created_at, deleted_at
from agent_enrollment_tokens;

drop table agent_enrollment_tokens;
alter table agent_enrollment_tokens_new rename to agent_enrollment_tokens;

create unique index idx_agent_enrollment_tokens_hash on agent_enrollment_tokens (token_hash) where deleted_at is null;
create index idx_agent_enrollment_tokens_org on agent_enrollment_tokens (organization_uid) where deleted_at is null;

-- Cluster-wide replay guard for agent reconnect signatures (the per-process
-- nonce cache is only sound with a single API replica).
create table agent_nonces (
  agent_uid text not null,
  nonce     text not null,
  seen_at   text not null,
  primary key (agent_uid, nonce)
);

create index agent_nonces_seen_at_idx on agent_nonces (seen_at);

-- ---------------------------------------------------------------------------
-- Per-status-page custom CSS (spec 2026-07-27-02)
-- ---------------------------------------------------------------------------
-- See the postgres twin for rationale. Stored verbatim; the API caps the value
-- at 64 KB and rejects @import.

alter table status_pages add column custom_css text; -- Operator-authored CSS injected into the public status page as a <style> text node. NULL = none.

-- ---------------------------------------------------------------------------
-- Status page resources can target a check GROUP (spec 2026-08-01-03)
-- ---------------------------------------------------------------------------
-- See the postgres twin for the full rationale. SQLite has no ALTER COLUMN, so
-- dropping the NOT NULL on check_uid means rebuilding the table with the
-- established *_new pattern (same as the agents rebuild above). The XOR check
-- constraint and the two partial unique indexes mirror
-- maintenance_window_checks.

create table status_page_resources_new (
  uid             text primary key,
  section_uid     text not null references status_page_sections(uid) on delete cascade, -- Parent section
  check_uid       text references checks(uid) on delete cascade, -- Individual check to display; NULL when targeting a group
  check_group_uid text references check_groups(uid) on delete cascade, -- Check group shown as one aggregated component; NULL when targeting a check
  public_name     text, -- Override display name. NULL uses the check/group name
  explanation     text, -- Optional description visible on the public status page
  position        integer not null default 0, -- Display order within the section (lower = higher)
  created_at      text not null default (datetime('now')),
  updated_at      text not null default (datetime('now')),
  check ((check_uid is not null and check_group_uid is null)
      or (check_uid is null and check_group_uid is not null))
);

insert into status_page_resources_new (
  uid, section_uid, check_uid, check_group_uid, public_name, explanation, position, created_at, updated_at
)
select
  uid, section_uid, check_uid, null, public_name, explanation, position, created_at, updated_at
from status_page_resources;

drop table status_page_resources;
alter table status_page_resources_new rename to status_page_resources;

create unique index status_page_resources_section_check_idx
  on status_page_resources (section_uid, check_uid) where check_uid is not null;
create unique index status_page_resources_section_group_idx
  on status_page_resources (section_uid, check_group_uid) where check_group_uid is not null;
create index status_page_resources_check_idx
  on status_page_resources (check_uid) where check_uid is not null;
create index status_page_resources_group_idx
  on status_page_resources (check_group_uid) where check_group_uid is not null;


-- ---------------------------------------------------------------------------
-- Microsoft Teams bot: one Entra tenant maps to at most one connection.
-- See the PostgreSQL migration for the full rationale — the application-level
-- check is a read-then-write with no serialization, so the uniqueness
-- invariant is enforced here as well.
create unique index if not exists integrations_msteams_bot_tenant_idx
  on integrations (json_extract(settings, '$.tenant_id'))
  where type = 'msteams-bot'
    and deleted_at is null
    and coalesce(json_extract(settings, '$.tenant_id'), '') <> '';

-- ---------------------------------------------------------------------------
-- (append further v0.7.0 blocks below this line)
-- ---------------------------------------------------------------------------
