-- solidping — cost-aware, plan-weighted check scheduling (spec 2026-06-30-09).
-- Layers fairness onto the pull-based check_jobs queue:
--   * cost_ewma_ms          — EWMA of execution duration (timeouts pinned to the
--                             ceiling). Folded into the existing post-exec write;
--                             no new query on the hot path. Drives slow-lane
--                             classification and the cost-aware execution timeout.
--   * plan_weight           — denormalized plan tier (0 = free, higher = paid).
--                             Lets the scheduler reserve capacity for paid orgs
--                             and give them a deadline credit under contention.
--   * effective_scheduled_at — scheduled_at + cost_penalty − tier_credit, capped.
--                             The claim SELECT still GATES on scheduled_at but
--                             ORDERS by this column (D2/Option A): de-prioritization
--                             only bites under contention.
--
-- Off-by-default-equivalent: penalty/credit = 0 and the caps default to the pool
-- size, so a fresh DB reproduces today's pure-FIFO behaviour. Existing rows get
-- cost_ewma_ms=0, plan_weight=0, effective_scheduled_at backfilled to scheduled_at.

alter table check_jobs add column cost_ewma_ms double precision not null default 0;
alter table check_jobs add column plan_weight smallint not null default 0;
alter table check_jobs add column effective_scheduled_at timestamptz;

-- Backfill the deadline anchor for existing rows so ordering is stable from the
-- first claim after migration (NULLs would otherwise sort unpredictably).
update check_jobs set effective_scheduled_at = scheduled_at where effective_scheduled_at is null;

-- The claim path filters on scheduled_at <= now and orders by
-- effective_scheduled_at; index the ordering column so the small due-set sort
-- stays cheap (see make bench-checks).
create index if not exists idx_check_jobs_effective_scheduled_at
    on check_jobs (effective_scheduled_at);

comment on column check_jobs.cost_ewma_ms is 'EWMA of execution duration in ms; timeouts pinned to the ceiling. Updated in the post-exec write. 0 until first run.';
comment on column check_jobs.plan_weight is 'Denormalized plan tier from org_entitlements. 0 = free; higher = more protected (reserved capacity + deadline credit). Refreshed on entitlement change + reconcile.';
comment on column check_jobs.effective_scheduled_at is 'scheduled_at + cost_penalty − tier_credit, capped. Claim ORDER BY key (gate stays on scheduled_at). Backfilled to scheduled_at.';
