-- Teardown/parity only — never run in production. Reverses 007_v0_6_0.up.sql.

drop table if exists agent_enrollment_tokens;
drop table if exists agents;
