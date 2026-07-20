-- solidping v0.6.0 — consolidated release migration.
--
-- Multi-region full-period scheduling (spec 2026-07-20-05): a check's period
-- now applies PER region — every selected region runs the check at the full
-- period, staggered by an inter-region offset — instead of the old scheme that
-- divided the period across regions (basePeriod × region_count per job). The
-- new optional `region_spread` column overrides that inter-region offset; the
-- default (NULL) is `period / region_count` (even coverage across the period).
--
-- It is a first-class scheduling input on `checks` (nullable interval, same
-- storage convention as `checks.period`) — it drives `check_jobs` creation, so
-- it is a column, not a `checks.config` key.

alter table checks add column region_spread interval;

comment on column checks.region_spread is
  'Optional inter-region scheduling offset (spec 2026-07-20-05). NULL = default of period / region_count (even coverage). 0 fires all regions together; must satisfy 0 <= region_spread < period.';
