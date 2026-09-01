-- v0.22.0 — the ONE consolidated migration for the (still unreleased) v0.22.0
-- release. 017_v0_21_0 is the last RELEASED migration, so everything this
-- cycle produces lands here, in a single file per dialect, per the repo
-- convention documented in wiki/conventions/database.md.
--
--   SECTION: heartbeat-push-counters   heartbeat_counters

-- ==========================================================================
-- SECTION: heartbeat-push-counters
--
-- The embedded TCP/UDP heartbeat transport's SP2 message form is replay-proof
-- because every beat carries a strictly-increasing counter and the server
-- remembers the last one it accepted. Without that memory an SP2 beat is no
-- better than SP1: a captured datagram could be replayed forever to mask a
-- real outage.
--
-- The state lives in its own table rather than on `checks` for two reasons:
-- it is written on every accepted signed beat (a hot, tiny write that has no
-- business touching the row the whole application reads), and a check that
-- never uses SP2 never gets a row at all.
--
-- Advancing the counter is a single conditional upsert
-- (ON CONFLICT ... DO UPDATE ... WHERE last_counter < excluded.last_counter),
-- so strict monotonicity is enforced by the database itself and stays correct
-- under concurrent beats from a device that retries a datagram.
--
-- No foreign key to checks: checks are soft-deleted, so a cascade would never
-- fire, and a stale row here is harmless (it is only ever read for a check
-- that is currently accepting beats). Keeping the row also means a check that
-- is undeleted does not silently regain acceptance of every old datagram.
-- ==========================================================================

create table if not exists heartbeat_counters (
  check_uid varchar(36) primary key,
  last_counter bigint not null,
  updated_at timestamptz not null default now()
);

--bun:split

comment on table heartbeat_counters is
  'Last accepted SP2 replay counter per heartbeat check (spec 2026-09-01-06). Advanced only by a conditional upsert that requires strictly-greater.';
