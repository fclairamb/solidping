-- v0.41.0 — SQLite rollback (parity only; downs are never run in production).

-- SECTION: discovered-checks-region
drop index if exists idx_discovered_checks_identity_active;

create unique index idx_discovered_checks_identity_active
  on discovered_checks (organization_uid, source, group_key, slug)
  where deleted_at is null and promoted_to_check_uid is null;

alter table discovered_checks drop column region;

-- SECTION: user-preferences
alter table users drop column preferences;
