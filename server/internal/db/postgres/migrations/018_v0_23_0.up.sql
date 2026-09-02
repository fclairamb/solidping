-- v0.23.0 — the ONE consolidated migration for the (still unreleased) v0.23.0
-- release, one file per dialect, per the repo convention documented in
-- wiki/conventions/database.md.
--
-- v0.22.0 is the last RELEASED version and it shipped with 017_v0_21_0 as its
-- final migration: a file is named after the release it ships IN, and no
-- migration was needed for v0.22.0, which is why the numbers and the versions
-- do not advance in lockstep. 017 is therefore frozen and everything this
-- cycle produces is appended here. The target is v0.23.0 rather than v0.22.1
-- because v0.22.1 is already sitting in an open release-please PR and this
-- cycle carries feat: commits, so the next minor is the release this DDL
-- actually reaches.
--
--   SECTION: passive-signal-index          results results_raw_signal_idx

-- ==========================================================================
-- SECTION: passive-signal-index
--
-- A passive check (heartbeat, email) interleaves two kinds of raw row:
-- SIGNAL rows written at ingest (handlers/heartbeat recordBeat,
-- handlers/emailcheck) which never set worker_uid, and EVALUATION rows
-- written once per period by checkworker.executePassiveJob through
-- DirectBackend.SubmitResult, which always does.
--
-- The evaluator now asks specifically for the newest SIGNAL row
-- (GetLastSignalForChecks, spec 2026-09-02-03) instead of the newest row of
-- any origin, which it was previously re-anchoring on — turning overdue
-- detection into a per-tick coin flip on claim jitter and making the
-- stale-run branch unreachable.
--
-- results_raw_idx (organization_uid, check_uid, period_start desc) WHERE
-- period_type = 'raw' cannot serve that lookup efficiently: it does not carry
-- worker_uid, so for a check that has stopped beating the descent walks every
-- evaluation row inside raw retention before it finds the beat — 1440 heap
-- fetches per tick at a 1-minute period with the 24 h default, every minute,
-- for every silent check.
--
-- This partial index contains only ingest rows and creation markers, so it is
-- tiny, and it makes the signal lookup a single descent regardless of how
-- long the check has been silent.
-- ==========================================================================

create index if not exists results_raw_signal_idx
  on results (organization_uid, check_uid, period_start desc)
  where period_type = 'raw' and worker_uid is null;
