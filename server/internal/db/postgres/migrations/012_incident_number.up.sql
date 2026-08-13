-- Scratch migration for the current release cycle (011 shipped with v0.14.0 and
-- is frozen). Adds the short, human-scale incident reference: a per-org,
-- monotonically increasing number, GitHub-issue style.
--
-- Why a column and not an ephemeral "index into the last listing": the mapping
-- has to be durable (restarts, several API pods, weeks later), queryable
-- (`ORDER BY number`, no N+1 lookups when rendering a list) and reversible in
-- both directions on every render path.
--
-- Soft-deleted rows KEEP their number and the unique index covers them, so a
-- number is never reused — `#42` means one incident forever.

alter table incidents add column if not exists number bigint not null default 0;

--bun:split

-- Backfill: per organization, ordered by started_at (uid breaks ties so the
-- assignment is deterministic and the migration is re-runnable). Only rows that
-- still carry the placeholder 0 are touched.
with ranked as (
  select uid,
         row_number() over (partition by organization_uid order by started_at, uid) as n
  from incidents
)
update incidents
set number = ranked.n
from ranked
where incidents.uid = ranked.uid
  and incidents.number = 0;

--bun:split

create unique index if not exists incidents_organization_number_idx
  on incidents (organization_uid, number);
