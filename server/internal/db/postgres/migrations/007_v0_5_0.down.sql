-- Reverse of 007_v0_5_0.up.sql. Teardown/parity only — never run in production.
-- The orphan purge is not reversible (deleted rows are gone and, being
-- FK-orphans, could not be re-inserted anyway), so this is a no-op.
select 1;
