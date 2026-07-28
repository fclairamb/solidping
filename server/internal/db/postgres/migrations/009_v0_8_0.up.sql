-- solidping v0.8.0 — consolidated release migration.
--
-- Multiple features share this one release migration; each contributes a
-- clearly separated block below. Append new blocks at the end.

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
-- (append further v0.8.0 blocks below this line)
-- ---------------------------------------------------------------------------
