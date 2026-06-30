-- solidping — adaptive recovery: flapping backoff with a cap (spec 2026-06-30-07).
-- SQLite mirror of the Postgres migration. Replaces the dead
-- `max_adaptive_increase` knob with a real time-based flapping model: when a
-- check flaps (repeated outages over a short horizon), require progressively
-- longer stability before auto-resolving each successive incident — bounded by
-- a cap and reset after a calm window.
--
-- Off-by-default-equivalent: flap_backoff_factor=1 or flapping_window_seconds=0
-- reproduces today's constant recovery_period_seconds behaviour.
-- (DROP COLUMN requires SQLite >= 3.35; the bundled engine supports it, as the
-- 004 migration already relies on.)

alter table checks add column flapping_window_seconds integer not null default 21600; -- 6h rolling flap window. 0 = adaptive recovery off
alter table checks add column flap_backoff_factor integer not null default 2; -- Per-flap recovery multiplier. 1 = off
alter table checks add column max_recovery_multiplier integer not null default 8; -- Cap on required-recovery vs base recovery period
alter table checks add column flap_count integer not null default 0; -- Outages in the rolling window; written only on incident open/reopen
alter table checks add column last_outage_at text; -- Wall-clock of most recent outage onset; gates the window reset. NULL until first outage

-- The old `max_adaptive_increase` was dead (consumed by nothing); its stored
-- values are meaningless, so we drop it rather than backfill.
alter table checks drop column max_adaptive_increase;
