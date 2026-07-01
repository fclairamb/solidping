-- Rollback for the per-job scheduling-delay EWMA column (extends spec 2026-06-30-09).
-- (DROP COLUMN requires SQLite >= 3.35; the bundled engine supports it, as 004–006 rely on.)

alter table check_jobs drop column delay_ewma_ms;
