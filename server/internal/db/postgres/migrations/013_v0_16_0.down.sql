-- Down migration for the worker capability set. Teardown/parity only.

alter table workers drop constraint if exists workers_capabilities_shape;

--bun:split

alter table workers drop column if exists capabilities;
