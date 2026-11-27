-- v0.57.0 — sqlite migration.
--
--   SECTION: worker-capabilities-unique   nothing to do on SQLite
--
-- The Postgres file replaces the back-reference regex of
-- workers_capabilities_shape. SQLite never had it: its triggers
-- (013_v0_16_0) find duplicates with json_each and count(distinct), which stays
-- cheap, and they only fire on an update of the capabilities column.
select 1;
