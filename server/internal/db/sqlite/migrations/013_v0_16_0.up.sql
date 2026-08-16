-- SQLite mirror of postgres/migrations/013_v0_16_0.up.sql — per-worker
-- capability set (specs 2026-08-15-11, 2026-08-16-02). See the Postgres file
-- for why the column is nullable with no backfill, and why NULL, '[]' and a
-- populated array are three genuinely different answers.
--
-- SQLite has no array type, so the set is a JSON array of the capability names
-- the worker HAS: NULL = never reported (unknown), '[]' = reported and has
-- none, '["ipv4","ipv6"]' = reported this exact set.
--
-- SQLite has no `ADD COLUMN IF NOT EXISTS`, so these statements are not
-- re-runnable. That is fine: bun records the applied migration by its numeric
-- prefix, and 013 is free on every install that has applied 012_v0_15_0. A
-- database that already carries the columns without the matching
-- `bun_migrations` row fails loudly, which is the correct visible failure mode.

-- An earlier draft of THIS SAME migration (never released) added two boolean
-- columns, `egress_ipv4` / `egress_ipv6`; it was rewritten in place because bun
-- keys applied migrations on the numeric prefix alone. A dev database that
-- already applied the old 013 will not re-run this one and must be reset
-- (SP_DB_RESET) rather than debugged.

-- The column CHECK covers the container: NULL, or a syntactically valid JSON
-- array. The ELEMENT rules cannot live here — SQLite forbids subqueries in a
-- CHECK constraint and json_each is table-valued — so they live in the two
-- triggers below, which together reject exactly what the Postgres CHECK
-- rejects.
alter table workers add column capabilities text
  check (
    capabilities is null
    or (json_valid(capabilities) and json_type(capabilities) = 'array')
  );

--bun:split

-- Element rules, byte-for-byte the same verdicts as the Postgres regex pair:
--   * every element must be a JSON string (rejects `[null]`, `[1]`, `[[]]`)
--   * no empty-string element
--   * names limited to the `[a-z0-9-]` slug charset (rejects `IPv6`, `ip_v6`,
--     `a,b`, ` `)
--   * the name `null` is RESERVED — see the Postgres file: `array_out` quotes
--     any element equal to "NULL" case-insensitively, so Postgres cannot store
--     the string "null" without it looking like the NULL element. Nothing here
--     would stop it, which is exactly the cross-dialect divergence the parity
--     table exists to catch, so it is rejected explicitly. `lower()` is
--     belt-and-braces: the charset rule already forces lowercase.
--   * no duplicates
create trigger workers_capabilities_shape_insert
before insert on workers
for each row when new.capabilities is not null
begin
  select raise(abort, 'workers.capabilities: not a set of lowercase capability slugs')
  where exists (
    select 1 from json_each(new.capabilities)
    where json_each.type <> 'text'
       or json_each.value = ''
       or json_each.value glob '*[^a-z0-9-]*'
       or lower(json_each.value) = 'null'
  )
  or (select count(*) from json_each(new.capabilities))
     <> (select count(distinct json_each.value) from json_each(new.capabilities));
end;

--bun:split

create trigger workers_capabilities_shape_update
before update of capabilities on workers
for each row when new.capabilities is not null
begin
  select raise(abort, 'workers.capabilities: not a set of lowercase capability slugs')
  where exists (
    select 1 from json_each(new.capabilities)
    where json_each.type <> 'text'
       or json_each.value = ''
       or json_each.value glob '*[^a-z0-9-]*'
       or lower(json_each.value) = 'null'
  )
  or (select count(*) from json_each(new.capabilities))
     <> (select count(distinct json_each.value) from json_each(new.capabilities));
end;
