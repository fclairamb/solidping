-- v0.16.0 — per-worker capability set (specs 2026-08-15-11, 2026-08-16-02).
--
-- Workers self-report what they can do — today the address families they can
-- originate traffic over — so the region list can tell a user "this region does
-- IPv6" BEFORE they create a check that fails on its first run.
--
-- ONE GENERIC SET, NOT ONE COLUMN PER CAPABILITY. The region layer has always
-- modelled capabilities as an open map keyed by name; storing them as a text[]
-- of the names a worker HAS makes the next capability (`udp`, `icmp`, `http3`,
-- …) a pure string addition with no migration.
--
-- THE COLUMN IS NULLABLE AND THERE IS DELIBERATELY NO BACKFILL, BECAUSE THIS
-- CARRIES THREE STATES, NOT TWO:
--
--   NULL          unknown — nothing was ever reported (an agent predating the
--                 feature, or a worker that has not checked in yet)
--   '{}'          reported, and this worker has none of them
--   '{ipv4,...}'  reported this exact set
--
-- NULL IS THE ONLY UNKNOWN. Once a worker reports at all its array is
-- authoritative and CLOSED: absence from a non-NULL array means "no", never
-- "unknown". Defaulting the column to '{}' would claim every existing worker
-- has no IPv6 — the exact false statement this change exists to remove — and
-- keeping NULL distinct from '{}' is what makes the rollout order (server first
-- or agent first) irrelevant.

-- An earlier draft of THIS SAME migration (never released — the latest tag is
-- v0.15.1 and 013 is the pending v0.16.0 delta) added two boolean columns,
-- `egress_ipv4` / `egress_ipv6`. It was rewritten in place rather than
-- superseded by a 014, because bun keys applied migrations on the numeric
-- prefix alone. Consequence: a DEV database that already applied the old 013
-- will NOT re-run this one and keeps the stale boolean columns while the code
-- expects `capabilities`. Reset such a database (SP_DB_RESET) — do not debug it.

alter table workers add column if not exists capabilities text[];

--bun:split

comment on column workers.capabilities is
  'Self-reported capability names the worker HAS. NULL = never reported (unknown); ''{}'' = reported and has none. Absence from a non-NULL array means "no", never "unknown".';

--bun:split

-- The whole risk of moving from typed boolean columns to a stringly-typed set
-- is that garbage becomes representable, so the shape is enforced by the
-- database rather than trusted to the writers — the CHECK buys back the safety
-- the `boolean` type used to give for free.
--
-- Postgres FORBIDS SUBQUERIES IN A CHECK, so `count(distinct …)` over
-- `unnest()` is not available and both rules are expressed over the array's own
-- text rendering:
--
--   1. shape/charset — `{a,b-c,d9}` or `{}`. This single regex rejects, in one
--      go, out-of-charset names (`{IPv6}`, `{ip_v6}`), empty-string elements
--      (rendered `{""}`), names containing the separator (`{"a,b"}`), NULL
--      elements (rendered `{a,NULL}` — uppercase, out of charset) and
--      multi-dimensional arrays (`{{a}}`). Any element needing quoting fails,
--      which is precisely the set of elements that are not bare slugs.
--
--      THE NAME `null` IS THEREBY RESERVED, AND THAT IS DELIBERATE. `array_out`
--      quotes any element equal to "NULL" case-insensitively, so the *string*
--      "null" renders as `{"null"}` and this regex refuses it — which is the
--      only thing that keeps the rendering unambiguous, since a BARE `NULL` in
--      the text form is how an actual NULL element is spelled. A capability
--      literally named `null` is not plausible, and both the SQLite triggers
--      and models.ValidateCapabilitySet reject it explicitly so all three
--      layers agree; see `dbcaptest.SharedCases`.
--   2. duplicates — a back-reference regex: some element, delimited by `{` or
--      `,`, occurring again later in the same array.
--
-- The SQLite mirror enforces exactly the same verdicts through triggers over
-- json_each (SQLite forbids subqueries in CHECK too, and json_each is
-- table-valued). A value one dialect refuses must not be storable in the other;
-- `TestWorkerCapabilitiesConstraintParity*` pins that both ways.
alter table workers drop constraint if exists workers_capabilities_shape;

--bun:split

alter table workers add constraint workers_capabilities_shape check (
  capabilities is null
  or (
    capabilities::text ~ '^\{([a-z0-9-]+(,[a-z0-9-]+)*)?\}$'
    and capabilities::text !~ '(\{|,)([a-z0-9-]+)(,[a-z0-9-]+)*,\2(,|\})'
  )
);
