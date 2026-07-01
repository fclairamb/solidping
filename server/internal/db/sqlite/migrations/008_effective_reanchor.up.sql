-- solidping — heal delay-era effective_scheduled_at offsets (spec 2026-07-01-02).
-- SQLite mirror of the Postgres migration.
--
-- Migration 007's delay term made the deprioritization offset unbounded: the
-- delay EWMA fed back into itself under overload and effective values drifted
-- far past scheduled_at (up to ~95 min observed), stranding jobs beyond the
-- claim window. The offset is now cost-only and clamped to 60 s, the claim
-- gate is back on the real scheduled_at, and delay_ewma_ms becomes pure
-- telemetry (probe start − scheduled_at).
--
-- Re-anchor every polluted row to its real schedule; the bounded cost-only
-- offset repopulates on each job's next post-exec release. Idempotent; rows
-- where effective <= scheduled_at (tier credit or fresh anchor) are untouched.

update check_jobs set effective_scheduled_at = scheduled_at
where effective_scheduled_at > scheduled_at; -- delay-era offsets; cost offset reapplies on next release
