-- Down migration for the worker egress capability. Teardown/parity only.
-- SQLite mirror of postgres/migrations/013_v0_16_0.down.sql.

alter table workers drop column egress_ipv6;

--bun:split

alter table workers drop column egress_ipv4;
