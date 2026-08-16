-- Consolidated release migration for v0.14.0.
--
-- Per wiki/conventions/database.md, each release contributes exactly ONE
-- migration file per engine. This one replaces the two scratch migrations of
-- the v0.14.0 cycle, which have been deleted:
--
--   011_user_integration_identities  — the Slack member-identity table (spec 2026-08-12-03)
--   012_private_region_org_relative  — private regions become org-relative (spec 2026-08-13-01)
--
-- It keeps the number 011 rather than taking a new one: bun keys applied
-- migrations on the NUMERIC PREFIX alone (`^(\d{1,14})_([0-9a-z_\-]+)\.`, see
-- bun/migrate/migrations.go), so a database that already applied the scratch
-- 011/012 — i.e. a dev box that ran this branch before consolidation — treats
-- this file as applied and does not re-run it. Its schema is already correct.
-- Taking 013 instead would make such a database re-run the DDL below. Installs
-- at v0.13.0 and earlier have applied 001–010 only, see "011" as unapplied, and
-- get both parts exactly once.
--
-- Part 2 is a DATA migration, not DDL, and is REQUIRED on every existing
-- install: it is what repairs regions stranded by a past org rename. Do not
-- drop it as "the fresh-database schema doesn't need it".
--
-- Every statement here is idempotent and the file is re-runnable. Note that bun
-- wraps a migration file in a transaction ONLY when its name ends in
-- `.tx.up.sql` (bun/migrate/migration.go); this one does not. On Postgres the
-- multi-statement simple query gets an implicit server-side transaction, which
-- is atomicity by accident rather than by design — the idempotency is what
-- actually makes a partial apply safe.

-- ===========================================================================
-- PART 1 — user_integration_identities (was 011_user_integration_identities)
-- ===========================================================================
--
-- Answers "who is this org member on this integration instance" — e.g. which
-- Slack user id to render as <@U123ABC> when an alert lands in a channel.
--
-- Deliberately NOT user_contacts: contacts are "how to page me" and carry
-- verification state; identities are "who I am there" and are used for
-- mentions and attribution only. Nothing in the paging path reads this table,
-- so a wrong row can annoy but can never misdirect a page.
--
-- Scoped per INTEGRATION rather than per integration type, because an org can
-- connect several Slack workspaces and the same human has a different user id
-- in each. The two unique indexes make the mapping a bijection inside one
-- integration: a member has at most one identity, and an external id belongs
-- to at most one member.

create table if not exists user_integration_identities (
  uid              uuid primary key default gen_random_uuid(),
  organization_uid uuid not null references organizations(uid) on delete cascade,
  integration_uid  uuid not null references integrations(uid) on delete cascade,
  user_uid         uuid not null references users(uid) on delete cascade,
  external_id      text not null,
  display_name     text not null default '',
  source           text not null default 'auto',
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now(),
  constraint user_integration_identities_source_check
    check (source in ('auto', 'manual'))
);

create unique index if not exists user_integration_identities_integration_user_idx
  on user_integration_identities (integration_uid, user_uid);
create unique index if not exists user_integration_identities_integration_external_idx
  on user_integration_identities (integration_uid, external_id);
create index if not exists user_integration_identities_organization_idx
  on user_integration_identities (organization_uid);

comment on table user_integration_identities is
  'Maps an org member to their provider-side identity on ONE integration instance (e.g. their Slack user id on a specific workspace). Used for mentions and attribution only — never for paging.';
comment on column user_integration_identities.integration_uid is
  'The integration instance this identity is valid on. An org with two Slack workspaces has one row per workspace.';
comment on column user_integration_identities.user_uid is
  'The SolidPing user this identity belongs to.';
comment on column user_integration_identities.external_id is
  'Provider-side identifier used to address the person in a message (Slack user id, rendered as <@id>).';
comment on column user_integration_identities.display_name is
  'Provider-side display name captured at match time. Cosmetic: shown in the admin UI and used for the plain-text fallback.';
comment on column user_integration_identities.source is
  'How the mapping was established: auto (email auto-match) or manual (an admin picked it). A re-sync never overwrites a manual row.';

-- ===========================================================================
-- PART 2 — private regions become ORG-RELATIVE (was 012_private_region_org_relative)
-- ===========================================================================
--
-- Private (deported-agent) regions used to be stored fully-qualified as
-- `@<org-slug>/<region-slug>`, built from the org slug AT WRITE TIME and then
-- denormalized into six places. Private regions match on EXACT string equality,
-- so renaming an org left every stored copy stale while the API started
-- advertising a new spelling no agent was bound to: every check created after a
-- rename sat in `validating` forever, silently. (Observed live: `acmetech` →
-- `acme`.)
--
-- The org is now implicit — every row carrying a region also carries an
-- organization_uid — so the stored form is `@<region-slug>` and a rename
-- touches nothing. This rewrites `@<anything>/<slug>` to `@<slug>` everywhere,
-- which ALSO retroactively repairs installs already broken by a past rename:
-- both spellings collapse onto the same org-relative string.
--
-- Collapsing two spellings onto one can violate a UNIQUE index, so each such
-- table is de-duplicated BEFORE its rewrite.
--
-- No re-sealing is required: credential envelopes are age/X25519 blobs sealed
-- to agent public keys (internal/crypto/credentials/sealing.go). The region
-- string is not an AAD, not part of any key derivation, and not stored in the
-- envelope — see wiki/conventions/regions.md.

-- ---------------------------------------------------------------------------
-- 2.1 check_jobs — UNIQUE (check_uid, region) where region is not null.
--     A check that listed both `@old/paris` and `@new/paris` has two dispatch
--     rows that become one. Keep the earliest-scheduled row; the scheduler
--     reconciles the rest on the next check write.
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
-- 2.2 agents.region and agent_enrollment_tokens.region — no unique index on
--     region, so a plain rewrite. This is what un-strands the agents enrolled
--     before a rename.
-- ---------------------------------------------------------------------------
update agents
set region = '@' || split_part(region, '/', 2),
    updated_at = now()
where region like '@%/%';

update agent_enrollment_tokens
set region = '@' || split_part(region, '/', 2)
where region like '@%/%';

-- ---------------------------------------------------------------------------
-- 2.3 checks.regions (text[]) — rewrite each element, then de-duplicate
--     ORDER-PRESERVINGLY: a check listing both spellings of the same region must
--     end up with exactly ONE entry (two would immediately re-break the unique
--     index on check_jobs).
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
-- 2.4 The `default_regions` parameter (org-level and system-level), stored as
--     {"value": [...]} jsonb. Same rewrite + order-preserving de-duplication.
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
-- 2.5 results.region — rewritten in the SAME migration so per-region
--     aggregation series stay continuous across the change instead of splitting
--     into a "before" and an "after" series.
--
--     DECISION (explicit, per spec): synchronous, one statement, NOT batched.
--     The predicate only matches rows produced by a deported agent, so the write
--     volume is tiny; the cost is one sequential scan of `results` at migration
--     time. Batching would not remove that scan (there is no index on `region`),
--     so it would buy nothing but complexity. Escape hatch for an install where a
--     one-pass scan at boot is unacceptable: run the same UPDATE out of band
--     beforehand — it is idempotent, and the migration then finds nothing to do.
--
--     results is UNIQUE on (organization_uid, check_uid, coalesce(region,''),
--     period_type, period_start) for aggregated rows, so collapsed keys are
--     de-duplicated first. Keep the row with the most data (largest
--     total_checks) — losing the smaller sibling bucket beats failing the
--     migration.
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
