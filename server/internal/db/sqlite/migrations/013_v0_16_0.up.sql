-- SQLite mirror of postgres/migrations/013_v0_16_0.up.sql — per-worker egress
-- capability (spec 2026-08-15-11). See the Postgres file for why both columns
-- are nullable with no backfill (null means "unknown", never "no IPv6").
--
-- SQLite has no `ADD COLUMN IF NOT EXISTS`, so these statements are not
-- re-runnable. That is fine: bun records the applied migration by its numeric
-- prefix, and 013 is free on every install that has applied 012_v0_15_0. A
-- database that already carries the columns without the matching
-- `bun_migrations` row fails loudly on "duplicate column name", which is the
-- correct visible failure mode.

alter table workers add column egress_ipv4 boolean;

--bun:split

alter table workers add column egress_ipv6 boolean;
