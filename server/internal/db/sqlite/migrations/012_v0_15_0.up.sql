-- SQLite mirror of postgres/migrations/012_v0_15_0.up.sql — the short,
-- human-scale per-org incident reference (`#42`). See the Postgres file for the
-- rationale, including why this is a scratch 012.
--
-- SQLite has no `ADD COLUMN IF NOT EXISTS`, so this statement is the one part
-- of the file that is not re-runnable. That is fine: bun records the applied
-- migration by its numeric prefix, and 012 is free on every install that has
-- applied the consolidated 011_v0_14_0. A database that already carries the
-- column must have the matching `bun_migrations` row, or this fails loudly on
-- "duplicate column name" — which is the correct, visible failure mode.

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
