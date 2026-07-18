-- Teardown/parity only — never run in production. Reverses 007_v0_6_0.up.sql.

alter table checks drop column config_sealed;
alter table check_jobs drop column config_sealed;

alter table workers add column token text;
create index idx_workers_token on workers (token);

drop table if exists agent_enrollment_tokens;
drop table if exists agents;
