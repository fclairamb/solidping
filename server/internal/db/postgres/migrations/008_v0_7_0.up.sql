-- solidping v0.7.0 — consolidated release migration.
--
-- Multiple features share this one release migration; each contributes a
-- clearly separated block below. Append new blocks at the end.

-- ---------------------------------------------------------------------------
-- Custom domains for status pages (spec 2026-07-22-01)
-- ---------------------------------------------------------------------------
-- A status page can be served on a customer-owned hostname
-- (e.g. status.acme.com), DNS-verified with a SINGLE CNAME that carries both
-- routing and ownership (see internal/domainverify: shared target, or a
-- per-page "<token>.cname.<target>" in token mode). One domain per page. The GLOBAL partial unique index
-- is the ownership-race anchor: it stops one org claiming another org's live
-- domain, and a unique violation on write maps to 409 CONFLICT — mirroring
-- status_pages_org_slug_idx from 001_v0_1_0.up.sql:694.

alter table status_pages add column custom_domain varchar(253);
alter table status_pages add column custom_domain_token varchar(64);
alter table status_pages add column custom_domain_verified_at timestamptz;
alter table status_pages add column custom_domain_checked_at timestamptz;
alter table status_pages add column custom_domain_failures smallint not null default 0;

create unique index status_pages_custom_domain_idx
  on status_pages (custom_domain)
  where custom_domain is not null and deleted_at is null;

comment on column status_pages.custom_domain is
  'Customer-owned hostname (punycode/ASCII, lowercased) the page is served on. NULL = none. Globally unique among live rows.';
comment on column status_pages.custom_domain_token is
  'Opaque, DNS-label-safe token (lowercase base32). Set while a domain is configured; in token mode it is the leading label of the expected CNAME target, <token>.cname.<installation target>.';
comment on column status_pages.custom_domain_verified_at is
  'When the domain last passed CNAME verification. NULL = unverified; only verified+enabled+public pages are served on the custom host.';
comment on column status_pages.custom_domain_checked_at is
  'When the periodic re-verification job last checked this domain.';
comment on column status_pages.custom_domain_failures is
  'Consecutive re-verification failures. At 3 the verification is cleared (domain release/takeover protection).';


-- ---------------------------------------------------------------------------
-- Phone (SMS/voice) contact verification (spec 2026-07-22-02)
-- ---------------------------------------------------------------------------
-- Per-user phone contacts are created unverified. A 6-digit code (SHA-256
-- hashed, 10-min expiry, attempt-capped) is issued via the org's Twilio
-- connection and confirmed here; only a verified number is ever texted/dialed
-- by the escalation dispatcher.
alter table user_contacts add column verify_code_hash varchar(64);
alter table user_contacts add column verify_expires_at timestamptz;
alter table user_contacts add column verify_attempts smallint not null default 0;

comment on column user_contacts.verify_code_hash is
  'SHA-256 hex of the in-flight 6-digit verification code. NULL when no verification is pending or after a successful confirm.';
comment on column user_contacts.verify_expires_at is
  'Expiry of the in-flight verification code (issue + 10 minutes).';
comment on column user_contacts.verify_attempts is
  'Failed confirm attempts for the in-flight code; at 5 the code is invalidated.';

-- ---------------------------------------------------------------------------
-- Monthly SMS/voice usage counters (spec 2026-07-22-02)
-- ---------------------------------------------------------------------------
-- Persistent per-org, per-kind, per-month counter. The reserve-then-send
-- conditional upsert (INSERT … ON CONFLICT DO UPDATE SET count = count + 1
-- WHERE count < ? RETURNING count) atomically claims one unit before a send so
-- a burst can never exceed the monthly cap. period_start is the first day of
-- the UTC month.
create table org_usage_counters (
  organization_uid varchar(36) not null,
  kind varchar(32) not null,
  period_start date not null,
  count integer not null default 0,
  primary key (organization_uid, kind, period_start)
);

-- ---------------------------------------------------------------------------
-- Results table tier-1 storage trim (spec 2026-07-24-02)
-- ---------------------------------------------------------------------------
-- The `results` table is the largest and hottest table in the system. Two of
-- its columns are pure waste and are dropped here:
--
--   * last_for_status — a write-only flag. Every result insert ran a companion
--     UPDATE clearing the predecessor's flag (dead tuple + index churn + WAL
--     per row), yet nothing ever read the column: the dashboard's "latest per
--     check" uses DISTINCT ON (check_uid), and the only WHERE last_for_status
--     was the maintenance UPDATE itself. Its partial index goes with it. Drop
--     the index first — Postgres rejects dropping a column an index still
--     references.
--
--   * availability_pct — fully derivable as successful_checks / total_checks
--     × 100, now computed at read time (handlers/results, availability, badges).
--     Storing it was redundant and one more way for the two to disagree.

drop index if exists idx_results_last_for_status;
alter table results drop column last_for_status;
alter table results drop column availability_pct;

-- ---------------------------------------------------------------------------
-- In-server ACME/TLS asset storage (spec 2026-07-26-01)
-- ---------------------------------------------------------------------------
-- certmagic needs a certmagic.Storage implementation to hold ACME account
-- keys, certificates, private keys, OCSP staples and distributed-challenge
-- tokens. Putting it in the database (rather than a volume) means a cluster
-- shares one set of certificates, a pod restart never re-issues, and there is
-- no extra volume to provision.
--
-- The keyspace is certmagic's own path-like namespace ("certificates/<ca>/
-- <domain>/<domain>.crt", "acme/<ca>/users/...", …) — slash-separated, no
-- leading/trailing slash. Prefix queries drive List/Delete, hence the
-- text_pattern_ops index: the default collation's opclass cannot serve a
-- LIKE 'prefix%' range scan on a non-C locale database.
--
-- SECURITY: rows in this table contain PRIVATE KEYS. Nothing may expose them
-- through any API, export, or debug surface (see the spec's security
-- checklist); only internal/tlsedge reads them.

create table tls_storage (
  key text primary key,
  value bytea not null,
  modified_at timestamptz not null default now()
);

create index tls_storage_key_prefix_idx on tls_storage (key text_pattern_ops);

comment on table tls_storage is
  'certmagic key-value store for TLS assets (ACME accounts, certificates, PRIVATE KEYS, OCSP staples). Never expose via any API.';
comment on column tls_storage.key is
  'certmagic storage key: slash-separated path, no leading/trailing slash.';
comment on column tls_storage.value is
  'Opaque asset bytes as written by certmagic (PEM, JSON, DER, …).';
comment on column tls_storage.modified_at is
  'Last write time — surfaced as certmagic KeyInfo.Modified and used for renewal decisions.';

-- Cluster-wide advisory locks for certmagic's Locker interface. certmagic only
-- needs coarse, single-writer-per-key locking around issuance/renewal, so an
-- expiring row is enough: the holder refreshes expires_at while it works, and a
-- crashed holder's lock is reclaimable once it goes stale.
create table tls_storage_locks (
  key text primary key,
  owner text not null,
  expires_at timestamptz not null
);

create index tls_storage_locks_expires_idx on tls_storage_locks (expires_at);

comment on table tls_storage_locks is
  'Expiring distributed locks backing certmagic Locker. A stale row (expires_at in the past) may be taken over by any node.';
comment on column tls_storage_locks.owner is
  'Opaque per-process identity; Unlock only releases a lock this process still owns.';

-- ---------------------------------------------------------------------------
-- Platform-operated "system agents" for cloud regions (spec 2026-07-27-01)
-- ---------------------------------------------------------------------------
-- Deported agents were hard-scoped to ONE organization's private region. To run
-- SolidPing's OWN cloud check workers outside the cluster (fly.io) over the very
-- same WebSocket transport — instead of exposing PostgreSQL to them — an agent
-- now carries a `kind`:
--
--   'org'    — the existing tenant-private agent: bound to exactly one org and
--              one `@<org>/<region>` slug. Behavior unchanged.
--   'system' — platform-operated: bound to a SHARED cloud region slug and to no
--              organization at all, so it claims that region's jobs across every
--              org — the same scope the in-cluster DirectBackend uses.
--
-- organization_uid therefore becomes nullable, with a check constraint keeping
-- the two facts in lockstep: kind = 'org' <=> organization_uid is not null.

alter table agents add column kind text not null default 'org';
alter table agents alter column organization_uid drop not null;

alter table agents add constraint agents_kind_check
  check (kind in ('org', 'system'));
alter table agents add constraint agents_kind_org_check
  check ((kind = 'org') = (organization_uid is not null));

create index idx_agents_kind_region on agents (kind, region) where deleted_at is null;

comment on column agents.kind is
  'org = tenant-private agent bound to one org + private region; system = platform-operated agent serving a shared cloud region across all orgs (no owning org).';
comment on column agents.organization_uid is
  'Owning organization. NULL exactly when kind = ''system'' (platform-operated agents have no tenant).';

-- Enrollment tokens gain the same org/system split. A system token is
-- MULTI-USE and revocable: every fly machine generates its own keypair and
-- enrolls on boot, so no private key is ever shared between machines, and the
-- fleet can scale without an operator minting one token per machine. Org
-- tokens stay strictly one-shot — `max_uses` is forbidden on them, and the
-- atomic `used_at IS NULL` consume is untouched.
alter table agent_enrollment_tokens add column kind text not null default 'org';
alter table agent_enrollment_tokens alter column organization_uid drop not null;
alter table agent_enrollment_tokens add column max_uses integer;
alter table agent_enrollment_tokens add column use_count integer not null default 0;

alter table agent_enrollment_tokens add constraint agent_enrollment_tokens_kind_check
  check (kind in ('org', 'system'));
alter table agent_enrollment_tokens add constraint agent_enrollment_tokens_kind_org_check
  check ((kind = 'org') = (organization_uid is not null));
alter table agent_enrollment_tokens add constraint agent_enrollment_tokens_one_shot_check
  check (kind = 'system' or max_uses is null);

comment on column agent_enrollment_tokens.kind is
  'org = one-shot token minted by an org admin; system = multi-use platform token seeded from SP_SYSTEM_AGENT_ENROLLMENT_TOKENS and bound to a cloud region slug.';
comment on column agent_enrollment_tokens.max_uses is
  'Enrollment budget for a system token (NULL = unlimited). Never set on an org token, which is strictly one-shot.';
comment on column agent_enrollment_tokens.use_count is
  'How many agents enrolled with this token. Always 0 or 1 for an org token.';

-- Shared reconnect-nonce replay guard. The agent reconnect signature covers
-- method|path|timestamp|nonce within a bounded clock skew; the nonce cache that
-- makes a replay impossible used to be per-process memory, which is only sound
-- with a single API replica. A fly fleet reconnects against whichever replica
-- the load balancer picks, so the guard moves into the database.
create table agent_nonces (
  agent_uid uuid        not null,
  nonce     text        not null,
  seen_at   timestamptz not null default now(),
  primary key (agent_uid, nonce)
);

create index agent_nonces_seen_at_idx on agent_nonces (seen_at);

comment on table agent_nonces is
  'Cluster-wide replay guard for agent reconnect signatures. Rows live only for the clock-skew retention window and are pruned by the agent_gc job.';

-- ---------------------------------------------------------------------------
-- Per-status-page custom CSS (spec 2026-07-27-02)
-- ---------------------------------------------------------------------------
-- status0 paints everything from CSS custom properties (--brand, --background,
-- --card, --border, the status colors, the .dark variant), so a single
-- per-page stylesheet is the whole re-skinning surface: no component change,
-- and a power-user escape hatch on top. Stored verbatim; the API caps it at
-- 64 KB and rejects @import (which would chain third-party stylesheets).
-- The value is served on the PUBLIC status-page responses — it is the public
-- renderer that consumes it.

alter table status_pages add column custom_css text;

comment on column status_pages.custom_css is
  'Operator-authored CSS injected into the public status page (status0) as a <style> text node. NULL = none. Capped at 64 KB, @import rejected at the API.';

-- ---------------------------------------------------------------------------
-- (append further v0.7.0 blocks below this line)
-- ---------------------------------------------------------------------------
