-- v0.39.0 — the ONE consolidated Postgres migration for the (still unreleased)
-- v0.39.0 release. 026_v0_38_0 shipped and is frozen, so this is the next free
-- number; every schema change of this cycle is appended here as a SECTION.
--
--   SECTION: check-deleted-incidents
--                              resolve every active incident whose check is
--                              already soft-deleted (resolution_type
--                              'check_deleted')
--   SECTION: check-job-run-request
--                              check_jobs.run_requested_at, a "Run now"
--                              request recorded while the job is leased
--   SECTION: incident-reminders
--                              checks.reminder_interval_seconds,
--                              incidents.reminder_count and
--                              incidents.last_reminder_at

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

--bun:split

-- ==========================================================================
-- SECTION: check-job-run-request (spec 2026-10-12-05)
-- ==========================================================================
-- "Run now" on a job a worker holds used to be dropped: the release rewrites
-- scheduled_at, so the request could not be written there. It is now parked
-- here, and every release makes the job due at it, then clears it. A check
-- edit that lands mid-run uses it too, so the edited config runs right after.
alter table check_jobs add column if not exists run_requested_at timestamptz;

--bun:split

comment on column check_jobs.run_requested_at is
  'Pending run request recorded while the job was leased ("Run now" or a check edit, spec 2026-10-12-05); the release makes the job due at it and clears it. NULL: none pending.';

--bun:split

-- ==========================================================================
-- SECTION: incident-reminders (spec 2026-10-12-16)
-- ==========================================================================
-- An open, unacknowledged incident re-notifies the check's channels every
-- interval until it is acknowledged, snoozed or resolved. The check value
-- wins; NULL inherits the org parameter incidents.reminder_interval_seconds
-- (absent = 0 = off). reminder_count is the compare-and-set token that lets
-- exactly one reminder job win per interval.
alter table checks add column if not exists reminder_interval_seconds integer;

--bun:split

comment on column checks.reminder_interval_seconds is
  'Re-notify interval for an open, unacknowledged incident (spec 2026-10-12-16). NULL: inherit the org default; 0: off; else 900..86400 seconds.';

--bun:split

alter table incidents add column if not exists reminder_count integer not null default 0;

--bun:split

alter table incidents add column if not exists last_reminder_at timestamptz;

--bun:split

comment on column incidents.reminder_count is
  'Reminders sent for this incident (spec 2026-10-12-16). Bumped by compare-and-set, so one reminder job wins per interval.';

--bun:split

comment on column incidents.last_reminder_at is
  'When the last reminder went out (spec 2026-10-12-16). NULL: none yet.';
