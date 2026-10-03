-- Teardown/parity half of 026_v0_38_0.up.sql.

-- SECTION: multistep-bulk-checks
drop index if exists idx_check_jobs_claim_bulk;
--bun:split
alter table check_jobs drop column step_failures;
--bun:split
alter table check_jobs drop column step_state_file_uid;
--bun:split
alter table check_jobs drop column step_count;
--bun:split
alter table check_jobs drop column step_run_started_at;
--bun:split
alter table check_jobs drop column step_run_uid;

-- SECTION: results-duration-p50
alter table results drop column duration_p50;
