-- v0.22.0 — the ONE consolidated migration for the (still unreleased) v0.22.0
-- release. 017_v0_21_0 is the last RELEASED migration, so everything this
-- cycle produces lands here, in a single file per dialect, per the repo
-- convention documented in wiki/conventions/database.md.
--
--   SECTION: heartbeat-push-counters   heartbeat_counters

-- ==========================================================================
-- SECTION: heartbeat-push-counters
--
-- SQLite half of the PostgreSQL migration of the same name — see
-- postgres/migrations/018_v0_22_0.up.sql for the full rationale. In short: the
-- SP2 beat form is replay-proof only because the server remembers the last
-- counter it accepted per check, and the advance is a conditional upsert so
-- the "strictly greater" rule is enforced by the database rather than by a
-- read-then-write that two concurrent beats could interleave.
--
-- integer here is SQLite's 64-bit rowid-class integer, matching the bigint on
-- PostgreSQL; timestamps are stored as text, as everywhere else in this
-- schema.
-- ==========================================================================

create table if not exists heartbeat_counters (
  check_uid varchar(36) primary key,
  last_counter integer not null,
  updated_at timestamp not null default current_timestamp
);
