-- Rollback for the cost-aware, plan-weighted scheduling columns (spec 2026-06-30-09).
-- (DROP COLUMN requires SQLite >= 3.35; the bundled engine supports it, as 004/005 rely on.)

drop index if exists idx_check_jobs_effective_scheduled_at;

alter table check_jobs drop column effective_scheduled_at;
alter table check_jobs drop column plan_weight;
alter table check_jobs drop column cost_ewma_ms;
