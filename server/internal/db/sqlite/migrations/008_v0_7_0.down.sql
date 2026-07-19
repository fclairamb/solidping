-- Teardown/parity only — never run in production. Reverses 008_v0_7_0.up.sql.

alter table organizations drop column default_escalation_policy_uid;
