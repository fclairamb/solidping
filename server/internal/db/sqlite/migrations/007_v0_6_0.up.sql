-- solidping v0.6.0 — see the postgres twin for the full rationale.
--
-- Multi-region full-period scheduling (spec 2026-07-20-05): every selected
-- region now runs the check at the full period, staggered by an inter-region
-- offset, instead of dividing the period across regions. Adds the optional
-- `region_spread` override column (NULL = default of period / region_count).
-- Stored as text (HH:MM:SS), the same cross-database convention as
-- `checks.period`.

alter table checks add column region_spread text; -- Inter-region offset override. NULL = default (period / region_count).
