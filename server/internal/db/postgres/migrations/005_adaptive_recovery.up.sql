-- solidping — adaptive recovery: flapping backoff with a cap (spec 2026-06-30-07).
-- Replaces the dead `max_adaptive_increase` knob with a real time-based
-- flapping model: when a check flaps (repeated outages over a short horizon),
-- require progressively longer stability before auto-resolving each successive
-- incident — bounded by a cap and reset after a calm window.
--
-- Off-by-default-equivalent: flap_backoff_factor=1 or flapping_window_seconds=0
-- reproduces today's constant recovery_period_seconds behaviour.

alter table checks add column flapping_window_seconds integer not null default 21600; -- 6h
alter table checks add column flap_backoff_factor integer not null default 2;
alter table checks add column max_recovery_multiplier integer not null default 8;
alter table checks add column flap_count integer not null default 0;
alter table checks add column last_outage_at timestamptz;

-- The old `max_adaptive_increase` was dead (consumed by nothing); its stored
-- values are meaningless, so we drop it rather than backfill.
alter table checks drop column max_adaptive_increase;

comment on column checks.flapping_window_seconds is 'Rolling window over which outages accumulate the recovery backoff. 0 = adaptive recovery off (constant recovery).';
comment on column checks.flap_backoff_factor is 'Multiplies required recovery time per flap inside the window. 1 = off (constant recovery).';
comment on column checks.max_recovery_multiplier is 'Cap: required recovery never exceeds this multiple of the base recovery period (and a 30m hard ceiling).';
comment on column checks.flap_count is 'Outages accumulated inside the rolling flapping window. Written only on incident open/reopen.';
comment on column checks.last_outage_at is 'Wall-clock of the most recent outage onset; gates the flapping-window reset. NULL until the first outage.';
