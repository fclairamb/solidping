-- Consolidated release migration for v0.14.0.
-- SQLite mirror of postgres/migrations/011_v0_14_0.up.sql — keep the two in
-- lockstep. See that file for the full rationale (including why this keeps the
-- number 011 rather than taking 013, and why PART 2 must not be dropped as
-- "the fresh-database schema doesn't need it"); only the dialect differs here:
--   * `split_part(region, '/', 2)`  ->  `substr(region, instr(region, '/') + 1)`
--   * `unnest(...) with ordinality` ->  `json_each(...)` (regions is a JSON text array)
--   * `DELETE ... USING`            ->  `DELETE ... WHERE uid IN (...)`
--
-- Replaces the two scratch migrations of the v0.14.0 cycle, now deleted:
--   011_user_integration_identities  — Slack member-identity table (spec 2026-08-12-03)
--   012_private_region_org_relative  — private regions become org-relative (spec 2026-08-13-01)

-- ===========================================================================
-- PART 1 — user_integration_identities (was 011_user_integration_identities)
-- ===========================================================================
--
-- user_integration_identities: who an org member is on ONE integration
-- instance (e.g. their Slack user id on a specific workspace). Used for
-- mentions and attribution only — the paging path never reads it.

create table if not exists user_integration_identities (
  uid              text primary key,
  organization_uid text not null references organizations(uid) on delete cascade,
  integration_uid  text not null references integrations(uid) on delete cascade, -- The integration instance this identity is valid on (one row per workspace)
  user_uid         text not null references users(uid) on delete cascade, -- The SolidPing user this identity belongs to
  external_id      text not null, -- Provider-side id used to address the person in a message (Slack user id, rendered as <@id>)
  display_name     text not null default '', -- Provider-side display name captured at match time; cosmetic + plain-text fallback
  source           text not null default 'auto', -- auto (email auto-match) or manual (an admin picked it); a re-sync never overwrites manual
  created_at       text not null default (datetime('now')),
  updated_at       text not null default (datetime('now')),
  check (source in ('auto', 'manual'))
);

--bun:split

create unique index if not exists user_integration_identities_integration_user_idx
  on user_integration_identities (integration_uid, user_uid);

--bun:split

create unique index if not exists user_integration_identities_integration_external_idx
  on user_integration_identities (integration_uid, external_id);

--bun:split

create index if not exists user_integration_identities_organization_idx
  on user_integration_identities (organization_uid);

--bun:split

-- ===========================================================================
-- PART 2 — private regions become ORG-RELATIVE (was 012_private_region_org_relative)
-- ===========================================================================
--
-- A DATA migration, required on every existing install: it is what repairs
-- regions stranded by a past org rename, and it retroactively collapses both
-- spellings onto one. Every statement is idempotent and re-runnable — this file
-- is not `.tx.up.sql`, so bun does not wrap it and SQLite runs the statements
-- one at a time; the idempotency is what makes a partial apply safe.

-- ---------------------------------------------------------------------------
-- 2.1 check_jobs — UNIQUE (check_uid, region). De-duplicate the rows that
--     collapse onto one another, keeping the earliest-scheduled.
-- ---------------------------------------------------------------------------
delete from check_jobs
where uid in (
  select uid from (
    select uid,
           row_number() over (
             partition by check_uid,
                          case when region like '@%/%'
                               then '@' || substr(region, instr(region, '/') + 1)
                               else region end
             order by scheduled_at asc nulls last, uid asc
           ) as rn
    from check_jobs
    where region like '@%'
  )
  where rn > 1
);

update check_jobs
set region = '@' || substr(region, instr(region, '/') + 1)
where region like '@%/%';

-- ---------------------------------------------------------------------------
-- 2.2 agents.region and agent_enrollment_tokens.region.
-- ---------------------------------------------------------------------------
update agents
set region = '@' || substr(region, instr(region, '/') + 1),
    updated_at = datetime('now')
where region like '@%/%';

update agent_enrollment_tokens
set region = '@' || substr(region, instr(region, '/') + 1)
where region like '@%/%';

-- ---------------------------------------------------------------------------
-- 2.3 checks.regions — a JSON text array in SQLite. Rewrite each element, then
--     de-duplicate order-preservingly (json_each.key is the array index).
-- ---------------------------------------------------------------------------
with expanded as (
  select c.uid as uid,
         je.key as ord,
         case when je.value like '@%/%'
              then '@' || substr(je.value, instr(je.value, '/') + 1)
              else je.value end as norm
  from checks c, json_each(c.regions) je
  where c.regions is not null
    and json_valid(c.regions)
    and json_type(c.regions) = 'array'
),
deduped as (
  select uid, norm, min(ord) as ord
  from expanded
  group by uid, norm
),
rebuilt as (
  select uid, json_group_array(norm order by ord) as regions
  from deduped
  group by uid
)
update checks
set regions = (select r.regions from rebuilt r where r.uid = checks.uid)
where exists (
  select 1 from rebuilt r
  where r.uid = checks.uid
    and r.regions is not checks.regions
);

-- ---------------------------------------------------------------------------
-- 2.4 The `default_regions` parameter, stored as {"value": [...]} JSON text.
-- ---------------------------------------------------------------------------
with expanded as (
  select p.uid as uid,
         je.key as ord,
         case when je.value like '@%/%'
              then '@' || substr(je.value, instr(je.value, '/') + 1)
              else je.value end as norm
  from parameters p, json_each(p.value, '$.value') je
  where p.key = 'default_regions'
    and json_valid(p.value)
    and json_type(p.value, '$.value') = 'array'
),
deduped as (
  select uid, norm, min(ord) as ord
  from expanded
  group by uid, norm
),
rebuilt as (
  select uid, json_group_array(norm order by ord) as arr
  from deduped
  group by uid
)
update parameters
set value = json_set(value, '$.value', json((select r.arr from rebuilt r where r.uid = parameters.uid))),
    updated_at = datetime('now')
where exists (
  select 1 from rebuilt r
  where r.uid = parameters.uid
    and r.arr is not json_extract(parameters.value, '$.value')
);

-- ---------------------------------------------------------------------------
-- 2.5 results.region — synchronous, one statement, not batched (see the
--     Postgres file for the decision and its rationale). Aggregated rows are
--     de-duplicated first because they are UNIQUE on
--     (organization_uid, check_uid, coalesce(region,''), period_type,
--     period_start); the row with the most data wins.
-- ---------------------------------------------------------------------------
delete from results
where uid in (
  select uid from (
    select uid,
           row_number() over (
             partition by organization_uid, check_uid,
                          case when region like '@%/%'
                               then '@' || substr(region, instr(region, '/') + 1)
                               else region end,
                          period_type, period_start
             order by total_checks desc nulls last, created_at desc, uid asc
           ) as rn
    from results
    where period_type <> 'raw'
      and region like '@%'
  )
  where rn > 1
);

update results
set region = '@' || substr(region, instr(region, '/') + 1)
where region like '@%/%';
