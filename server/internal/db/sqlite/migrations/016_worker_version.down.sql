-- Teardown/parity only — never run in production. Reverses
-- 016_worker_version.up.sql.
-- SQLite mirror of postgres/migrations/016_worker_version.down.sql.

alter table workers drop column version;
