-- Scratch migration for the current release cycle. Adds the short, human-scale
-- incident reference: a per-org, monotonically increasing number, GitHub-issue
-- style.
--
-- It takes 013, NOT 012, and that is load-bearing. bun keys applied migrations
-- on the NUMERIC PREFIX alone, and the v0.14.0 cycle burned BOTH 011 and 012 as
-- scratch migrations before they were consolidated into 011_v0_14_0 (see the
-- header of that file). Any database that ran the pre-consolidation branch —
-- every dev box, and any install deployed from it — already has a row for "012"
-- and would treat a 012 file here as applied, silently skipping the DDL below
-- and 500ing on the first incident query. 013 is the first genuinely free
-- number.
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
