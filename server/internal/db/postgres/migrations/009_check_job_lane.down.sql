-- Rollback for the fast/slow check lanes (spec 2026-07-01-03): drop the
-- per-lane partial indexes and the lane column, and restore the full
-- effective_scheduled_at ordering index the pre-lane claim query relies on.

drop index if exists idx_check_jobs_claim_fast;
drop index if exists idx_check_jobs_claim_slow;

create index if not exists idx_check_jobs_effective_scheduled_at
    on check_jobs (effective_scheduled_at);

alter table check_jobs drop column if exists lane;
