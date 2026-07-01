-- solidping — heal delay-era effective_scheduled_at offsets (spec 2026-07-01-02).
--
-- Migration 007's delay term made the deprioritization offset unbounded: under
-- sustained overload the delay EWMA fed back into itself and effective values
-- drifted up to ~95 min past scheduled_at (observed live), stranding jobs
-- beyond the claim window. The offset is now computed from the cost EWMA only,
-- clamped to 60 s, and the claim gate is back on the real scheduled_at
-- (delay_ewma_ms stays as pure telemetry, re-anchored to scheduled_at).
--
-- Re-anchor every polluted row to its real schedule in one idempotent
-- statement; the bounded cost-only offset repopulates on each job's next
-- post-exec release. Rows where effective <= scheduled_at (tier credit or
-- fresh anchor) are untouched.

update check_jobs set effective_scheduled_at = scheduled_at
where effective_scheduled_at > scheduled_at; -- delay-era offsets; cost offset reapplies on next release

comment on column check_jobs.delay_ewma_ms is 'EWMA of (probe start − scheduled_at) in ms, floored at 0. Pure telemetry (cost-distribution endpoint, lane-split go/no-go); never steers claim ordering. 0 until first run.';
comment on column check_jobs.effective_scheduled_at is 'scheduled_at + cost_ewma_ms×2 (clamped to 60s) − tier_credit. Claim ORDER BY key only; the gate is on scheduled_at. Backfilled to scheduled_at.';
