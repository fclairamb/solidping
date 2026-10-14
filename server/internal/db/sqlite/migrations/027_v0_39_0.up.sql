-- v0.39.0 — SQLite twin of the Postgres 027_v0_39_0 migration.
--
-- SECTION: check-deleted-incidents (spec 2026-10-08-02)
-- Resolve every active incident whose check is already soft-deleted, as of the
-- moment the check was deleted (resolution_type 'check_deleted'). Re-runnable.

update incidents
   set state = 2,
       resolved_at = (select c.deleted_at from checks c where c.uid = incidents.check_uid),
       resolution_type = 'check_deleted',
       updated_at = datetime('now')
 where state = 1
   and deleted_at is null
   and exists (
     select 1 from checks c
      where c.uid = incidents.check_uid
        and c.deleted_at is not null
   );
