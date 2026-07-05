-- solidping v0.2.1 — rollback for email_suppressions (reverse of 003_v0_2_1.up.sql).

drop table if exists email_suppressions;
