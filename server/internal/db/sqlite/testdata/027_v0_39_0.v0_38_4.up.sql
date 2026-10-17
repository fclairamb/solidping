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

--bun:split

-- SECTION: check-job-run-request (spec 2026-10-12-05)
-- A "Run now" (or a check edit) that lands while a worker holds the job is
-- parked here; every release makes the job due at it, then clears it.
alter table check_jobs add column run_requested_at text; -- Pending run request recorded while the job was leased; NULL: none pending

--bun:split

-- SECTION: incident-reminders (spec 2026-10-12-16)
-- Re-notify an open, unacknowledged incident every interval. NULL on the
-- check inherits the org parameter incidents.reminder_interval_seconds
-- (absent = 0 = off). reminder_count is the compare-and-set token.
alter table checks add column reminder_interval_seconds integer; -- NULL: inherit org default; 0: off; else 900..86400 seconds

--bun:split

alter table incidents add column reminder_count integer not null default 0; -- reminders sent, bumped by compare-and-set

--bun:split

alter table incidents add column last_reminder_at text; -- when the last reminder went out; NULL: none yet
