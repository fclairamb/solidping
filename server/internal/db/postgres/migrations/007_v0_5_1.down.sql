-- Teardown/parity only — never run in production. Reverses 007_v0_5_1.up.sql.
-- Guarded with IF EXISTS since this migration is itself a guarded backfill of
-- 006_v0_5_0 objects that may already have been dropped by 006's own down
-- migration.

alter table organizations drop column if exists default_escalation_policy_uid;

alter table checks     drop column if exists config_sealed;
alter table check_jobs drop column if exists config_sealed;

drop table if exists agent_enrollment_tokens;
drop table if exists agents;
