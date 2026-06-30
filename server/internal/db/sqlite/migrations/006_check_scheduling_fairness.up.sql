-- solidping — cost-aware, plan-weighted check scheduling (spec 2026-06-30-09).
-- SQLite mirror of the Postgres migration. Layers fairness onto the pull-based
-- check_jobs queue:
--   * cost_ewma_ms          — EWMA of execution duration in ms (timeouts pinned to
--                             the ceiling). Folded into the existing post-exec
--                             write; drives slow-lane classification and the
--                             cost-aware execution timeout.
--   * plan_weight           — denormalized plan tier (0 = free; higher = paid).
--                             Reserved capacity + deadline credit for paid orgs.
--   * effective_scheduled_at — scheduled_at + cost_penalty − tier_credit, capped.
--                             Claim ORDER BY key; gate stays on scheduled_at
--                             (D2/Option A) so de-prioritization only bites under
--                             contention.
--
-- Off-by-default-equivalent: penalty/credit = 0 and caps default to the pool
-- size, reproducing today's pure-FIFO behaviour. Existing rows get
-- cost_ewma_ms=0, plan_weight=0, effective_scheduled_at backfilled to scheduled_at.

alter table check_jobs add column cost_ewma_ms real not null default 0; -- EWMA of execution duration in ms; timeouts pinned to ceiling. 0 until first run
alter table check_jobs add column plan_weight integer not null default 0; -- Plan tier: 0 = free, higher = more protected. Refreshed on entitlement change + reconcile
alter table check_jobs add column effective_scheduled_at text; -- scheduled_at + cost_penalty − tier_credit, capped. Claim ORDER BY key

-- Backfill the deadline anchor so ordering is stable from the first claim.
update check_jobs set effective_scheduled_at = scheduled_at where effective_scheduled_at is null;

-- Index the ordering column so the small due-set sort stays cheap.
create index if not exists idx_check_jobs_effective_scheduled_at on check_jobs (effective_scheduled_at);
