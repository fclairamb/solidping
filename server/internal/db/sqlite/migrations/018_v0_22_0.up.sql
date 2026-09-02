-- v0.22.0 — the ONE consolidated migration for the (still unreleased) v0.22.0
-- release. 017_v0_21_0 is the last RELEASED migration, so everything this
-- cycle produces lands here, in a single file per dialect, per the repo
-- convention documented in wiki/conventions/database.md.
--
--   SECTION: passive-signal-index          results results_raw_signal_idx

-- ==========================================================================
-- SECTION: passive-signal-index
--
-- SQLite half of the PostgreSQL migration of the same name — see
-- postgres/migrations/018_v0_22_0.up.sql for the full rationale. In short:
-- the passive evaluator now looks up the newest INBOUND SIGNAL row
-- (worker_uid IS NULL) instead of the newest raw row of any origin, and
-- results_raw_idx does not carry worker_uid, so a silent heartbeat's descent
-- would walk every evaluation row inside raw retention on every tick.
--
-- SQLite supports partial indexes with exactly this shape (results_raw_idx
-- already is one), so the two dialects are textually identical here.
-- ==========================================================================

create index if not exists results_raw_signal_idx
  on results (organization_uid, check_uid, period_start desc)
  where period_type = 'raw' and worker_uid is null;
