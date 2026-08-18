-- v0.17.0 — self-healing re-application of 013_v0_16_0 (spec 2026-08-18-02).
--
-- WHY A SECOND MIGRATION FOR DDL 013 ALREADY CONTAINS. 013 was rewritten in
-- place, under the same number, after a running dev loop had already applied an
-- earlier draft of it. bun keys applied migrations on the numeric prefix alone,
-- so on such a database the rewritten statements NEVER RAN while
-- `bun_migrations` kept claiming 013 was applied: `workers.capabilities` and
-- its shape constraint were simply missing, every query touching the column
-- failed, region resolution 404'd and check scheduling stopped. 013 itself
-- cannot fix that — it will never be re-run — so the repair has to arrive as a
-- new number.
--
-- This file is therefore 013's DDL again, verbatim and idempotent. On a
-- correctly-migrated database every statement is a no-op; on a desynced one it
-- lands the missing column and constraint. After 014 the two databases are
-- schema-identical, which is the acceptance bar.
--
-- The rules the constraint encodes (why the set is a text[] of capability
-- names, why NULL / '{}' / '{ipv4,...}' are three different answers, why the
-- name `null` is reserved) are documented at length in 013_v0_16_0.up.sql and
-- are not repeated here.
--
-- Going forward the startup checksum guard (internal/db/migrationguard) makes
-- this failure mode loud rather than silent: an applied migration whose content
-- changed fails the boot instead of being skipped.

alter table workers add column if not exists capabilities text[];

--bun:split

comment on column workers.capabilities is
  'Self-reported capability names the worker HAS. NULL = never reported (unknown); ''{}'' = reported and has none. Absence from a non-NULL array means "no", never "unknown".';

--bun:split

alter table workers drop constraint if exists workers_capabilities_shape;

--bun:split

alter table workers add constraint workers_capabilities_shape check (
  capabilities is null
  or (
    capabilities::text ~ '^\{([a-z0-9-]+(,[a-z0-9-]+)*)?\}$'
    and capabilities::text !~ '(\{|,)([a-z0-9-]+)(,[a-z0-9-]+)*,\2(,|\})'
  )
);
