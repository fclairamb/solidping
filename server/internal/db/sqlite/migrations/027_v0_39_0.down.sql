-- Teardown/parity half of 027_v0_39_0.up.sql — never run in production.
--
-- SECTION: check-deleted-incidents
-- Data fix only, nothing to undo: resolved orphans stay resolved.
select 1;

--bun:split

-- SECTION: check-job-run-request
alter table check_jobs drop column run_requested_at;

--bun:split

-- SECTION: incident-reminders
alter table incidents drop column last_reminder_at;

--bun:split

alter table incidents drop column reminder_count;

--bun:split

alter table checks drop column reminder_interval_seconds;
