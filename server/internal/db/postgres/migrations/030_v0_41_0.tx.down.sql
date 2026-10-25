-- v0.41.0 — Postgres rollback (parity only; downs are never run in production).

-- SECTION: crawl-tasks
alter table worker_tasks drop column if exists units_granted;
--bun:split

-- SECTION: worker-tasks
drop table if exists worker_tasks;
--bun:split

-- SECTION: discovered-checks-region
drop index if exists idx_discovered_checks_identity_active;
--bun:split
create unique index idx_discovered_checks_identity_active
  on discovered_checks (organization_uid, source, group_key, slug)
  where deleted_at is null and promoted_to_check_uid is null;
--bun:split
alter table discovered_checks drop column if exists region;

-- SECTION: user-preferences
alter table users drop column if exists preferences;
