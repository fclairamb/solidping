-- Teardown/parity only — never run in production. Reverses
-- 018_slo_reporting.up.sql.
--
-- SQLite gained DROP COLUMN in 3.35; the results columns are dropped
-- individually, newest first.

drop table if exists report_schedules;

--bun:split

drop table if exists slos;

--bun:split

alter table results drop column maintenance_successful_checks;

--bun:split

alter table results drop column maintenance_checks;

--bun:split

alter table results drop column maintenance;
