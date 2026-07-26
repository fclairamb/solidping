-- solidping v0.8.0 — see the postgres twin for the full rationale.
--
-- Multiple features share this one release migration; each contributes a
-- clearly separated block below. Append new blocks at the end.

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
-- (append further v0.8.0 blocks below this line)
-- ---------------------------------------------------------------------------
