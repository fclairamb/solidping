-- Migration 012: private regions become ORG-RELATIVE (spec 2026-08-13-01).
-- SQLite mirror of postgres/migrations/012_private_region_org_relative.up.sql —
-- keep the two in lockstep. See that file (and wiki/conventions/regions.md) for
-- the full rationale; only the dialect differs here:
--   * `split_part(region, '/', 2)`  ->  `substr(region, instr(region, '/') + 1)`
--   * `unnest(...) with ordinality` ->  `json_each(...)` (regions is a JSON text array)
--   * `DELETE ... USING`            ->  `DELETE ... WHERE uid IN (...)`

-- ---------------------------------------------------------------------------
-- 1. check_jobs — UNIQUE (check_uid, region). De-duplicate the rows that
--    collapse onto one another, keeping the earliest-scheduled.
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
-- 2. agents.region and agent_enrollment_tokens.region.
-- ---------------------------------------------------------------------------
update agents
set region = '@' || substr(region, instr(region, '/') + 1),
    updated_at = datetime('now')
where region like '@%/%';

update agent_enrollment_tokens
set region = '@' || substr(region, instr(region, '/') + 1)
where region like '@%/%';

-- ---------------------------------------------------------------------------
-- 3. checks.regions — a JSON text array in SQLite. Rewrite each element, then
--    de-duplicate order-preservingly (json_each.key is the array index).
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
-- 4. The `default_regions` parameter, stored as {"value": [...]} JSON text.
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
-- 5. results.region — synchronous, one statement, not batched (see the
--    Postgres file for the decision, its rationale, and the note on atomicity:
--    this file is NOT `.tx.up.sql`, so bun does not wrap it and SQLite runs the
--    statements one at a time. Every statement here is idempotent and the file
--    is re-runnable, which is what makes a partial apply safe.) Aggregated rows are
--    de-duplicated first because they are UNIQUE on
--    (organization_uid, check_uid, coalesce(region,''), period_type,
--    period_start); the row with the most data wins.
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
