-- solidping — per-job scheduling-delay EWMA (extends spec 2026-06-30-09).
-- delay_ewma_ms is an EWMA of how late a job actually started relative to its
-- effective_scheduled_at (probe start − effective_scheduled_at, floored at 0).
-- It folds into the existing post-exec write (no new hot-path query) and feeds
-- the claim ORDER BY key:
--   effective_scheduled_at = scheduled_at + cost_ewma_ms + delay_ewma_ms − tier_credit
-- so a job that chronically starts late under contention sorts later (easing the
-- backlog), while the claim gate stays on the real scheduled_at (work-conserving:
-- spare capacity still runs everything on time). Existing rows get
-- delay_ewma_ms = 0 and converge from their next run.

alter table check_jobs add column delay_ewma_ms double precision not null default 0;

comment on column check_jobs.delay_ewma_ms is 'EWMA of (probe start − effective_scheduled_at) in ms, floored at 0. Folded into the post-exec write; added to effective_scheduled_at alongside cost_ewma_ms. 0 until first run.';
