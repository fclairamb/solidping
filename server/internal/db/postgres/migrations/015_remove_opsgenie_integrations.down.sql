-- Teardown/parity only — never run in production. Reverses
-- 015_remove_opsgenie_integrations.up.sql.
--
-- There is no schema to revert: the up migration only deleted data. Deleted
-- rows cannot be un-deleted, so this is intentionally a no-op — the same
-- spirit as every other hard-delete migration in this repo (pre-1.0, no
-- tombstones). A database that ran the up migration and then this down
-- migration is NOT restored to its prior state; it is simply marked as
-- having reversed the migration.

select 1;
