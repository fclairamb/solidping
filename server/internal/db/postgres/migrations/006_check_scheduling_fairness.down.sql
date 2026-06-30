-- Rollback for the cost-aware, plan-weighted scheduling columns (spec 2026-06-30-09).

drop index if exists idx_check_jobs_effective_scheduled_at;

alter table check_jobs drop column if exists effective_scheduled_at;
alter table check_jobs drop column if exists plan_weight;
alter table check_jobs drop column if exists cost_ewma_ms;
