-- Teardown/parity half of 028_v0_38_5.tx.up.sql — never run in production.

-- SECTION: incident-reminders
alter table checks drop column reminder_interval_seconds;

--bun:split

alter table incidents drop column reminder_count;

--bun:split

alter table incidents drop column last_reminder_at;

--bun:split

-- SECTION: check-job-run-request
alter table check_jobs drop column run_requested_at;
