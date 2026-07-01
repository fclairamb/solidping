-- solidping — per-job scheduling-delay EWMA (extends spec 2026-06-30-09).
-- SQLite mirror of the Postgres migration. delay_ewma_ms is an EWMA of how late
-- a job actually started relative to its effective_scheduled_at (probe start −
-- effective_scheduled_at, floored at 0), folded into the post-exec write and
-- added to effective_scheduled_at alongside cost_ewma_ms in the claim ORDER BY
-- key. The claim gate stays on scheduled_at. Existing rows get 0 and converge
-- from their next run.

alter table check_jobs add column delay_ewma_ms real not null default 0; -- EWMA of (probe start − effective_scheduled_at) in ms, floored at 0. 0 until first run
