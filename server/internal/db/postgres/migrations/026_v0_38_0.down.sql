-- Teardown/parity half of 026_v0_38_0.up.sql — never run in production.

-- ==========================================================================
-- SECTION: multistep-bulk-checks
-- ==========================================================================

drop index if exists idx_check_jobs_claim_bulk;
alter table check_jobs drop column if exists step_failures;
alter table check_jobs drop column if exists step_state_file_uid;
alter table check_jobs drop column if exists step_count;
alter table check_jobs drop column if exists step_run_started_at;
alter table check_jobs drop column if exists step_run_uid;

-- ==========================================================================
-- SECTION: results-duration-p50
-- ==========================================================================

alter table results drop column if exists duration_p50;
