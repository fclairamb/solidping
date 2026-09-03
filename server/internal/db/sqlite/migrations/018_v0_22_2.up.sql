-- v0.22.2 — the ONE consolidated migration for the (still unreleased) v0.22.2
-- release, one file per dialect, per the repo convention documented in
-- wiki/conventions/database.md.
--
-- v0.22.0 is the last RELEASED version and it shipped with 017_v0_21_0 as its
-- final migration: a file is named after the release it ships IN, and no
-- migration was needed for v0.22.0, which is why the numbers and the versions
-- do not advance in lockstep. 017 is therefore frozen and everything this
-- cycle produces is appended here, under a number (018) that is unreleased
-- and free.
--
-- The suffix is v0.22.2 because this batch ships as a PATCH release: the repo
-- squash-merges, so release-please sees only the batch PR title, and that
-- title is a fix:. v0.22.1 is already sitting in an open release-please PR
-- and this batch lands after it, so v0.22.2 is the release this DDL reaches.
--
--   SECTION: passive-signal-index          results results_raw_signal_idx

-- ==========================================================================
-- SECTION: passive-signal-index
--
-- SQLite half of the PostgreSQL migration of the same name — see
-- postgres/migrations/018_v0_22_2.up.sql for the full rationale. In short:
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
