-- Teardown/parity only — never run in production. Reverses 009_v0_8_0.up.sql.
-- Reverse order: later-appended feature blocks are torn down before the
-- earlier ones they were stacked on top of.

-- reverse in-server ACME/TLS asset storage (spec 2026-07-26-01)
-- Dropping tls_storage discards every issued certificate and account key; the
-- next start simply re-issues on demand.
drop table if exists tls_storage_locks;
drop table if exists tls_storage;
