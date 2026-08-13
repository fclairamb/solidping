-- Migration 012 down: deliberately a NO-OP. See the Postgres mirror
-- (postgres/migrations/012_private_region_org_relative.down.sql) — the up
-- migration is lossy (the org slug lived nowhere but inside the string it
-- removed), and re-deriving it from the row's organization_uid would rebuild
-- the very stale copy the migration exists to delete.

select 1;
