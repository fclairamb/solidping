-- Teardown/parity half of 028_v0_38_5.tx.up.sql — never run in production.

-- ==========================================================================
-- SECTION: incident-reminders
-- ==========================================================================

alter table checks drop column if exists reminder_interval_seconds;

--bun:split

alter table incidents drop column if exists reminder_count;

--bun:split

alter table incidents drop column if exists last_reminder_at;

--bun:split

-- ==========================================================================
-- SECTION: check-job-run-request
-- ==========================================================================

alter table check_jobs drop column if exists run_requested_at;
