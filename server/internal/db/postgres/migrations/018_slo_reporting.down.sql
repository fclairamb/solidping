-- Teardown/parity only — never run in production. Reverses
-- 018_slo_reporting.up.sql.

drop table if exists report_schedules;

--bun:split

drop table if exists slos;

--bun:split

alter table results drop column if exists maintenance_successful_checks;

--bun:split

alter table results drop column if exists maintenance_checks;

--bun:split

alter table results drop column if exists maintenance;
