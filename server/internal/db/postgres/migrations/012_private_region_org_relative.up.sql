-- Migration 012: private regions become ORG-RELATIVE (spec 2026-08-13-01).
--
-- Private (deported-agent) regions used to be stored fully-qualified as
-- `@<org-slug>/<region-slug>`, built from the org slug AT WRITE TIME and then
-- denormalized into six places. Private regions match on EXACT string equality,
-- so renaming an org left every stored copy stale while the API started
-- advertising a new spelling no agent was bound to: every check created after a
-- rename sat in `validating` forever, silently. (Observed live: `stonaltech` →
-- `stonal`.)
--
-- The org is now implicit — every row carrying a region also carries an
-- organization_uid — so the stored form is `@<region-slug>` and a rename
-- touches nothing. This migration rewrites `@<anything>/<slug>` to `@<slug>`
-- everywhere, which ALSO retroactively repairs installs already broken by a
-- past rename: both spellings collapse onto the same org-relative string.
--
-- Collapsing two spellings onto one can violate a UNIQUE index, so each such
-- table is de-duplicated BEFORE its rewrite.
--
-- No re-sealing is required: credential envelopes are age/X25519 blobs sealed
-- to agent public keys (internal/crypto/credentials/sealing.go). The region
-- string is not an AAD, not part of any key derivation, and not stored in the
-- envelope — see wiki/conventions/regions.md.

-- ---------------------------------------------------------------------------
-- 1. check_jobs — UNIQUE (check_uid, region) where region is not null.
--    A check that listed both `@old/paris` and `@new/paris` has two dispatch
--    rows that become one. Keep the earliest-scheduled row; the scheduler
--    reconciles the rest on the next check write.
-- ---------------------------------------------------------------------------
delete from check_jobs cj
using (
  select uid,
         row_number() over (
           partition by check_uid,
                        case when region like '@%/%'
                             then '@' || split_part(region, '/', 2)
                             else region end
           order by scheduled_at asc nulls last, uid asc
         ) as rn
  from check_jobs
  where region like '@%'
) dup
where cj.uid = dup.uid
  and dup.rn > 1;

update check_jobs
set region = '@' || split_part(region, '/', 2)
where region like '@%/%';

-- ---------------------------------------------------------------------------
-- 2. agents.region and agent_enrollment_tokens.region — no unique index on
--    region, so a plain rewrite. This is what un-strands the agents enrolled
--    before a rename.
-- ---------------------------------------------------------------------------
update agents
set region = '@' || split_part(region, '/', 2),
    updated_at = now()
where region like '@%/%';

update agent_enrollment_tokens
set region = '@' || split_part(region, '/', 2)
where region like '@%/%';

-- ---------------------------------------------------------------------------
-- 3. checks.regions (text[]) — rewrite each element, then de-duplicate
--    ORDER-PRESERVINGLY: a check listing both spellings of the same region must
--    end up with exactly ONE entry (two would immediately re-break the unique
--    index on check_jobs).
-- ---------------------------------------------------------------------------
with expanded as (
  select c.uid,
         u.ord,
         case when u.elem like '@%/%'
              then '@' || split_part(u.elem, '/', 2)
              else u.elem end as norm
  from checks c
  cross join lateral unnest(c.regions) with ordinality as u(elem, ord)
),
deduped as (
  select uid, norm, min(ord) as ord
  from expanded
  group by uid, norm
),
rebuilt as (
  select uid, array_agg(norm order by ord) as regions
  from deduped
  group by uid
)
update checks c
set regions = r.regions
from rebuilt r
where c.uid = r.uid
  and c.regions is distinct from r.regions;

-- ---------------------------------------------------------------------------
-- 4. The `default_regions` parameter (org-level and system-level), stored as
--    {"value": [...]} jsonb. Same rewrite + order-preserving de-duplication.
-- ---------------------------------------------------------------------------
with expanded as (
  select p.uid,
         e.ord,
         case when e.elem like '@%/%'
              then '@' || split_part(e.elem, '/', 2)
              else e.elem end as norm
  from parameters p
  cross join lateral jsonb_array_elements_text(p.value -> 'value') with ordinality as e(elem, ord)
  where p.key = 'default_regions'
    and jsonb_typeof(p.value -> 'value') = 'array'
),
deduped as (
  select uid, norm, min(ord) as ord
  from expanded
  group by uid, norm
),
rebuilt as (
  select uid, jsonb_agg(norm order by ord) as arr
  from deduped
  group by uid
)
update parameters p
set value = jsonb_set(p.value, '{value}', r.arr),
    updated_at = now()
from rebuilt r
where p.uid = r.uid
  and (p.value -> 'value') is distinct from r.arr;

-- ---------------------------------------------------------------------------
-- 5. results.region — rewritten in the SAME migration so per-region
--    aggregation series stay continuous across the change instead of splitting
--    into a "before" and an "after" series.
--
--    DECISION (explicit, per spec): synchronous, one statement, NOT batched.
--    The predicate only matches rows produced by a deported agent, so the write
--    volume is tiny; the cost is one sequential scan of `results` at migration
--    time. Batching would not remove that scan (there is no index on `region`),
--    so it would buy nothing but complexity. Escape hatch for an install where a
--    one-pass scan at boot is unacceptable: run the same UPDATE out of band
--    beforehand — it is idempotent, and the migration then finds nothing to do.
--
--    NOTE ON ATOMICITY — do not assume this file runs in a transaction. bun
--    wraps a migration file ONLY when its name ends in `.tx.up.sql`
--    (bun/migrate/migration.go); this one does not, so on SQLite the statements
--    run one at a time and a crash halfway leaves a partial apply. On Postgres
--    the multi-statement simple query gets an implicit server-side transaction,
--    which is atomicity by accident, not by design. What actually makes a
--    partial apply safe here is that EVERY statement above is idempotent and
--    the whole file is re-runnable: re-running finds nothing left matching
--    `@%/%` and no duplicates left to collapse. Keep it that way.
--
--    results is UNIQUE on (organization_uid, check_uid, coalesce(region,''),
--    period_type, period_start) for aggregated rows, so collapsed keys are
--    de-duplicated first. Keep the row with the most data (largest
--    total_checks) — losing the smaller sibling bucket beats failing the
--    migration.
-- ---------------------------------------------------------------------------
delete from results r
using (
  select uid,
         row_number() over (
           partition by organization_uid, check_uid,
                        case when region like '@%/%'
                             then '@' || split_part(region, '/', 2)
                             else region end,
                        period_type, period_start
           order by total_checks desc nulls last, created_at desc, uid asc
         ) as rn
  from results
  where period_type <> 'raw'
    and region like '@%'
) dup
where r.uid = dup.uid
  and dup.rn > 1;

update results
set region = '@' || split_part(region, '/', 2)
where region like '@%/%';
