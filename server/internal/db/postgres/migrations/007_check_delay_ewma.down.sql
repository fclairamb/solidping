-- Rollback for the per-job scheduling-delay EWMA column (extends spec 2026-06-30-09).

alter table check_jobs drop column if exists delay_ewma_ms;
