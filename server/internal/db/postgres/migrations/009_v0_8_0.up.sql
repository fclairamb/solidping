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
-- (append further v0.8.0 blocks below this line)
-- ---------------------------------------------------------------------------
