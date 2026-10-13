-- Teardown/parity half of 027_v0_39_0.up.sql — never run in production.

-- ==========================================================================
-- SECTION: check-deleted-incidents
-- ==========================================================================

-- The backfill is a data fix with no schema change: resolved orphans stay
-- resolved (re-opening them would restart their escalations).
select 1;

--bun:split

-- ==========================================================================
-- SECTION: check-job-run-request
-- ==========================================================================

alter table check_jobs drop column if exists run_requested_at;

--bun:split

-- ==========================================================================
-- SECTION: incident-reminders
-- ==========================================================================

alter table incidents drop column if exists last_reminder_at;

--bun:split

alter table incidents drop column if exists reminder_count;

--bun:split

alter table checks drop column if exists reminder_interval_seconds;
