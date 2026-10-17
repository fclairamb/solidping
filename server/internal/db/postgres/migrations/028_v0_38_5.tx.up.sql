-- v0.38.5 — Postgres migration.
--
-- These sections were first appended to 027_v0_39_0, which had already shipped
-- in v0.38.1, and released that way in v0.38.4. Databases that applied the
-- v0.38.1 copy of 027 never got them, and the migration guard refused to boot.
-- 027 is back to its released content and the sections live here. A database
-- that ran the v0.38.4 copy of 027 has them already: the guard marks this
-- migration applied for it (migrationguard.Superseded), so it does not run.
-- Every statement is still re-runnable.
--
-- Frozen once tagged, like every migration: a later change goes in 029.
--
--   SECTION: check-job-run-request
--                              check_jobs.run_requested_at, a "Run now"
--                              request recorded while the job is leased
--   SECTION: incident-reminders
--                              checks.reminder_interval_seconds,
--                              incidents.reminder_count and
--                              incidents.last_reminder_at

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
