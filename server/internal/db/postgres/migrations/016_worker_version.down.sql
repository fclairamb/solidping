-- Teardown/parity only — never run in production. Reverses
-- 016_worker_version.up.sql.

alter table workers drop constraint if exists workers_version_not_empty;

--bun:split

alter table workers drop column if exists version;
