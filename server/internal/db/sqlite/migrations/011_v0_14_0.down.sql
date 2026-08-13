-- Down migration for the consolidated v0.14.0 release migration.
-- Teardown/parity only — never run in production.
-- SQLite mirror of postgres/migrations/011_v0_14_0.down.sql.
--
-- PART 1 (user_integration_identities) reverses cleanly. PART 2 (private
-- regions become org-relative) is deliberately NOT reversed: the up migration is
-- lossy (the org slug lived nowhere but inside the string it removed), and
-- re-deriving it from the row's organization_uid would rebuild the very stale
-- copy the migration exists to delete.

drop index if exists user_integration_identities_organization_idx;

--bun:split

drop index if exists user_integration_identities_integration_external_idx;

--bun:split

drop index if exists user_integration_identities_integration_user_idx;

--bun:split

drop table if exists user_integration_identities;
