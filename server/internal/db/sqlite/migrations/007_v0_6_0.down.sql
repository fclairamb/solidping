-- Teardown/parity only — never run in production. Reverses 007_v0_6_0.up.sql.

-- reverse multi-region full-period scheduling (spec 2026-07-20-05)
alter table checks drop column region_spread;
