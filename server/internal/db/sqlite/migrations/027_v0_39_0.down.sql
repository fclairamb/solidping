-- Teardown/parity half of 027_v0_39_0.up.sql — never run in production.
--
-- SECTION: check-deleted-incidents
-- Data fix only, nothing to undo: resolved orphans stay resolved.
select 1;

--bun:split

-- SECTION: check-job-run-request
alter table check_jobs drop column run_requested_at;
