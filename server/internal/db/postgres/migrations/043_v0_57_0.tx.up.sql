-- v0.57.0 — postgres migration.
--
--   SECTION: worker-capabilities-unique   workers_capabilities_shape without the back-reference regex
--
-- ==========================================================================
-- SECTION: worker-capabilities-unique
-- ==========================================================================
-- workers_capabilities_shape (013_v0_16_0) found duplicate capabilities with a
-- back-reference regex over the array's text rendering. Postgres evaluates it
-- by backtracking, and its cost grows much faster than the set: ~400 ms per row
-- once agents reported one check-type-* capability per check type (52 entries,
-- v0.56.0). A CHECK runs on every update of the row, so every worker heartbeat
-- paid it.
--
-- The duplicate test is now an unnest-based function (a CHECK cannot hold a
-- subquery, but it can call an immutable function): about 1 ms at that size.
-- The shape regex stays: it has no back-reference and is linear. The accepted
-- and refused values do not change (dbcaptest.SharedCases pins them).
create or replace function workers_capabilities_unique(caps text[]) returns boolean
language sql immutable parallel safe
as $$ select count(*) = count(distinct c) from unnest(caps) as c $$;

--bun:split

comment on function workers_capabilities_unique(text[]) is
  'True when the capability array has no duplicate element. Used by the workers_capabilities_shape CHECK.';

--bun:split

alter table workers drop constraint if exists workers_capabilities_shape;

--bun:split

alter table workers add constraint workers_capabilities_shape check (
  capabilities is null
  or (
    capabilities::text ~ '^\{([a-z0-9-]+(,[a-z0-9-]+)*)?\}$'
    and workers_capabilities_unique(capabilities)
  )
);
