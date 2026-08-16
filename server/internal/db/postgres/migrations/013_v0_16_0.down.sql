-- Down migration for the worker egress capability. Teardown/parity only.

alter table workers drop column if exists egress_ipv6;

--bun:split

alter table workers drop column if exists egress_ipv4;
