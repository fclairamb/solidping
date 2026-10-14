-- v0.39.0 — the ONE consolidated Postgres migration for the (still unreleased)
-- v0.39.0 release. 026_v0_38_0 shipped and is frozen, so this is the next free
-- number; every schema change of this cycle is appended here as a SECTION.
--
--   SECTION: check-deleted-incidents
--                              resolve every active incident whose check is
--                              already soft-deleted (resolution_type
--                              'check_deleted')

-- SECTION: check-deleted-incidents (spec 2026-10-08-02)
-- Deleting a check now resolves its active incidents in the same transaction
-- (db DeleteCheck). Before that, org deletion and the test API soft-deleted
-- checks without touching their incidents, which stayed active (and kept
-- escalating) forever. Close those orphans as of the moment their check was
-- deleted. Re-runnable: a second run finds no active orphan.
update incidents i
   set state = 2,
       resolved_at = c.deleted_at,
       resolution_type = 'check_deleted',
       updated_at = now()
  from checks c
 where c.uid = i.check_uid
   and c.deleted_at is not null
   and i.state = 1
   and i.deleted_at is null;

--bun:split

comment on column incidents.resolution_type is
  'auto | manual | expired | escalated | disabled | check_deleted. NULL until resolved_at is set.';
