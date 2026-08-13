-- SQLite mirror of postgres/migrations/012_incident_number.up.sql — the short,
-- human-scale per-org incident reference (`#42`). See the Postgres file for the
-- rationale.
--
-- SQLite has no `ADD COLUMN IF NOT EXISTS`, so this statement is the one part
-- of the file that is not re-runnable. That is fine: bun records the applied
-- migration by its numeric prefix, and 012 is a fresh number in this cycle.

alter table incidents add column number bigint not null default 0;

--bun:split

-- Backfill: per organization, ordered by started_at (uid breaks ties so the
-- assignment is deterministic). A correlated count is used rather than
-- `update ... from` (SQLite 3.33+ only) so the migration runs on the oldest
-- engine the sqliteshim can resolve.
update incidents
set number = (
  select count(*)
  from incidents i2
  where i2.organization_uid = incidents.organization_uid
    and (i2.started_at < incidents.started_at
         or (i2.started_at = incidents.started_at and i2.uid <= incidents.uid))
)
where number = 0;

--bun:split

create unique index if not exists incidents_organization_number_idx
  on incidents (organization_uid, number);
