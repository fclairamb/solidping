-- Down migration for the worker capability set. Teardown/parity only.
-- SQLite mirror of postgres/migrations/013_v0_16_0.down.sql.

drop trigger if exists workers_capabilities_shape_update;

--bun:split

drop trigger if exists workers_capabilities_shape_insert;

--bun:split

alter table workers drop column capabilities;
