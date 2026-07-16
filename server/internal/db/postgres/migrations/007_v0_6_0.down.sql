-- Teardown/parity only — never run in production. Reverses 007_v0_6_0.up.sql.

alter table workers add column token text;
create index idx_workers_token on workers (token) where token is not null and deleted_at is null;

drop table if exists agent_enrollment_tokens;
drop table if exists agents;
