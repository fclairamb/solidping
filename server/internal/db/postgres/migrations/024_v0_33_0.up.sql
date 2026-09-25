-- v0.33.0 — the ONE consolidated Postgres migration for the (still unreleased)
-- v0.33.0 release. 023_v0_32_0 shipped, so this is the next free number and
-- every schema change of this cycle is appended here as a new SECTION, per
-- wiki/conventions/database.md.
--
--   SECTION: check-freshness   checks.last_result_at, its backfill and the
--                              freshness-sweep index
--   SECTION: passive-checks-no-regions
--                              heartbeat/email checks lose their regions and
--                              keep one NULL-region job
--   SECTION: auto-region-placement
--                              checks.placement / region_count / region_pool,
--                              and checks on the system default regions
--                              switch to automatic placement
--   SECTION: degraded-incident-kind
--                              incidents_kind_check accepts 'degraded'
--   SECTION: drop-degraded-dry-run
--                              checks.degraded_would_fire_at goes, the degraded
--                              sweep reads only degraded_enabled checks
--   SECTION: multi-region-quorum
--                              checks.fail_quorum and the per-(check, region)
--                              reading table check_region_states
--   SECTION: auth-handoff-codes
--                              single-use codes that hand a federated login's
--                              session to the dashboard (auth_handoff_codes)
--
-- ⚠️ A DEV DATABASE THAT ALREADY RAN AN EARLIER DRAFT OF THIS FILE MUST BE
-- RESET, NEVER REPAIRED. bun keys an applied migration on its numeric prefix
-- alone, so appending a section to 024 does not re-run it: set
-- `SP_DB_RESET=true` (test/demo run mode) or drop and recreate the database.
-- **Do not run `solidping migrate repair`** — it rewrites the recorded
-- checksum without applying anything.

-- ==========================================================================
-- SECTION: check-freshness  (spec 2026-09-25-02)
--
-- checks.status only changed when a result arrived, so a check whose results
-- stopped kept its last status forever: on 2026-09-24 a region was dark for
-- 8 hours and its 12 pinned checks stayed `up`. The freshness sweep now moves
-- such a check to the `stale` status (10, "No data"). To keep that sweep one
-- indexed query instead of a scan of `results`, the newest REAL result time is
-- denormalized onto the check row. It is written by
-- incidents.ProcessCheckResult for every up/down/timeout/error/warning result,
-- including for checks inside a maintenance window.
--
-- Backfill: the newest real raw row per check (results_raw_idx serves the
-- correlated MAX). A check with no raw row inside the raw retention stays NULL
-- and is measured from created_at — it produced nothing for a day, which is
-- exactly what the sweep exists to report.
-- ==========================================================================

alter table checks add column if not exists last_result_at timestamptz;

--bun:split

comment on column checks.last_result_at is
  'Execution time of the newest real result (up/down/timeout/error/warning) across every region. NULL when the check never produced one. Read by the freshness sweep (spec 2026-09-25-02).';

--bun:split

update checks c
   set last_result_at = (
     select max(r.period_start)
       from results r
      where r.organization_uid = c.organization_uid
        and r.check_uid = c.uid
        and r.period_type = 'raw'
        and r.status in (3, 4, 5, 6, 8)
   )
 where c.deleted_at is null;

--bun:split

-- The sweep's candidate predicate is
--   coalesce(last_result_at, created_at) < cutoff
-- over enabled, non-internal, live checks that are not already stale.
create index if not exists idx_checks_freshness
  on checks ((coalesce(last_result_at, created_at)))
  where deleted_at is null and enabled and not internal and status <> 10;

--bun:split

-- The column comment written in 001 predates every status after `down`.
comment on column checks.status is
  'Current derived check status: 1=created, 3=up, 4=down, 5=validating, 7=degraded (aggregated only), 8=warning, 10=stale (no recent real result).';

--bun:split

-- ==========================================================================
-- SECTION: passive-checks-no-regions  (spec 2026-09-25-04)
--
-- Passive checks (heartbeat, email) make no outbound request, so a region
-- added nothing but a place for their evaluator to die: a dark region
-- silenced every dead-man's switch pinned to it, and a region served by an
-- agent turned every evaluation into an error and an incident. They are now
-- evaluated on the jobs node from ONE job with a NULL region.
--
-- The type list mirrors checkerdef.PassiveCheckTypes().
--
-- The surviving job is converted rather than inserted so its scheduled_at
-- and plan weight carry over. Anything this leaves behind (a passive check
-- with no job at all) is healed by the boot repair
-- (ListChecksWithStaleJobRegions), which runs after migrations at every start.
-- ==========================================================================

update checks
   set regions = '{}'
 where type in ('heartbeat', 'email')
   and coalesce(array_length(regions, 1), 0) > 0;

--bun:split

-- A passive check that already owns its NULL-region job loses every regional
-- one.
delete from check_jobs cj
 where cj.type in ('heartbeat', 'email')
   and cj.region is not null
   and exists (
     select 1 from check_jobs n
      where n.check_uid = cj.check_uid
        and n.region is null
   );

--bun:split

-- Otherwise keep exactly one regional job per check (the lowest uid)...
delete from check_jobs cj
 where cj.type in ('heartbeat', 'email')
   and cj.region is not null
   and cj.uid::text <> (
     select min(k.uid::text) from check_jobs k
      where k.check_uid = cj.check_uid
        and k.region is not null
   );

--bun:split

-- ...and turn it into the NULL-region job, lease cleared so the jobs node can
-- claim it on its next pass.
update check_jobs
   set region = null,
       lease_worker_uid = null,
       lease_expires_at = null,
       lease_starts = 0,
       updated_at = now()
 where type in ('heartbeat', 'email')
   and region is not null;

--bun:split

-- ==========================================================================
-- SECTION: auto-region-placement  (spec 2026-09-25-06)
--
-- Regions were resolved once and frozen with no record of intent: every new
-- check was pinned to the org/system default regions, whether anybody chose
-- them or not, so one dark region stopped every check "pinned" there. A check
-- now records its placement intent:
--
--   placement     'pinned' (regions is the user's list, never moved) or
--                 'auto'   (regions is the CURRENT placement, rewritten by
--                           the region sweep when a placed region goes dark)
--   region_count  auto only: N, how many regions run the check
--   region_pool   auto only: candidate cloud slugs, NULL/empty = any
--
-- Keeping the auto placement in checks.regions is what keeps the boot repair,
-- reconcileCheckJobs, the phase computation and the rate accounting unchanged.
-- ==========================================================================

alter table checks add column if not exists placement text not null default 'pinned';

--bun:split

alter table checks drop constraint if exists checks_placement_valid;

--bun:split

alter table checks add constraint checks_placement_valid check (placement in ('pinned', 'auto'));

--bun:split

alter table checks add column if not exists region_count integer;

--bun:split

alter table checks add column if not exists region_pool text[];

--bun:split

comment on column checks.placement is
  'Placement intent: pinned (regions is the user''s explicit list, never moved) or auto (regions is the current placement, re-placed by the region sweep when a placed region goes dark). Spec 2026-09-25-06.';

--bun:split

comment on column checks.region_count is
  'Auto placement only: how many regions run the check. NULL for pinned checks.';

--bun:split

comment on column checks.region_pool is
  'Auto placement only: candidate cloud region slugs; NULL or empty means any cloud region.';

--bun:split

-- The one migration exception (resolved open question 1): a check whose
-- regions are exactly the system default_regions, as a set, was almost
-- certainly never placed by anybody — it is the "67 checks pinned to
-- gravelines" case. It becomes auto with region_count = its current region
-- count and an empty pool: same regions, same jobs, same cost, and it gains
-- failover. Every other check keeps placement = 'pinned' (the column default).
-- Passive types (checkerdef.PassiveCheckTypes) have no regions and never move;
-- a check naming a private (`@`) region is pinned-only.
update checks c
   set placement = 'auto',
       region_count = cardinality(c.regions),
       region_pool = null
  from parameters p
 where p.organization_uid is null
   and p.key = 'default_regions'
   and p.deleted_at is null
   and jsonb_typeof(p.value -> 'value') = 'array'
   and c.deleted_at is null
   and c.placement = 'pinned'
   and c.type not in ('heartbeat', 'email', 'private-location')
   and cardinality(c.regions) > 0
   and not exists (select 1 from unnest(c.regions) as r(slug) where r.slug like '@%')
   and cardinality(c.regions) = (select count(distinct x.slug) from unnest(c.regions) as x(slug))
   and (select array_agg(distinct x.slug order by x.slug) from unnest(c.regions) as x(slug))
     = (select array_agg(distinct d.slug order by d.slug)
          from jsonb_array_elements_text(p.value -> 'value') as d(slug));

--bun:split

-- ==========================================================================
-- SECTION: degraded-incident-kind  (spec 2026-09-24-08)
--
-- 015_v0_18_0 pinned incidents.kind to ('check', 'slo_burn'), and 023_v0_32_0
-- added the 'degraded' kind without widening it. On Postgres every degraded
-- incident insert therefore failed with a check violation (23514): degraded
-- detection shipped in v0.32 could not open a single incident there. SQLite
-- never had the constraint. Found while writing the drop-degraded-dry-run
-- migration test, which needs a degraded row to exist.
-- ==========================================================================

alter table incidents drop constraint if exists incidents_kind_check;

--bun:split

alter table incidents add constraint incidents_kind_check check (kind in ('check', 'slo_burn', 'degraded'));

--bun:split

-- ==========================================================================
-- SECTION: drop-degraded-dry-run  (spec 2026-09-24-08)
--
-- 023_v0_32_0 shipped degraded detection with a dry run: the evaluator swept
-- every check, and on one with degraded_enabled = false it opened nothing and
-- stamped degraded_would_fire_at, which fed a banner on the check page and a
-- "would have fired" filter on the checks list. The dry run is gone. Rollout
-- is now just the column default: off for every check that predates the
-- feature, on for new checks, and enabling it on an existing check is a
-- per-check decision. A disabled check is not evaluated at all.
--
-- 023 is released and frozen, so the column is dropped here rather than
-- withdrawn there.
-- ==========================================================================

alter table checks drop column if exists degraded_would_fire_at;

--bun:split

comment on column checks.degraded_enabled is
  'Whether degraded detection runs on this check. FALSE = not evaluated. Off for checks that predate the feature, on for new ones.';

--bun:split

-- The sweep now filters on degraded_enabled, and most pre-existing checks are
-- off: without the flag in the predicate the oldest-evaluated-first index scan
-- would wade through every disabled row to fill its batch.
drop index if exists idx_checks_degraded_eval;

--bun:split

create index if not exists idx_checks_degraded_eval
  on checks (degraded_evaluated_at)
  where deleted_at is null and enabled and degraded_enabled;

--bun:split

-- A degraded incident open on a check whose flag is already off (enabled,
-- then disabled, under v0.32) used to auto-resolve because the old sweep still
-- read that check. The new sweep never will, so close it now, the same way
-- turning the flag off does from now on.
update incidents i
   set state = 2,
       resolved_at = now(),
       resolution_type = 'disabled',
       updated_at = now()
 where i.kind = 'degraded'
   and i.state = 1
   and i.deleted_at is null
   and exists (
     select 1 from checks c
      where c.uid = i.check_uid
        and not c.degraded_enabled
   );

--bun:split

-- ==========================================================================
-- SECTION: multi-region-quorum  (spec 2026-09-25-10)
--
-- A multi-region check went through one state machine that ignored the
-- region: any passing result cleared the confirmation clock, so one failing
-- region never opened an incident and was never surfaced either.
--
--   checks.fail_quorum    how many regions must be failing for the check to be
--                         down: 'all', 'majority' or a positive integer. NULL =
--                         the default (all for 1-2 regions, majority for 3+),
--                         which keeps every existing check's behavior.
--   check_region_states   the newest real reading per (check, region), written
--                         by the incident pipeline for checks with 2+ regions.
--                         Only rows whose region is in the check's CURRENT
--                         regions count; a row left behind by an automatic
--                         re-placement is inert and never needs pruning.
-- ==========================================================================

alter table checks add column if not exists fail_quorum text;

--bun:split

alter table checks drop constraint if exists checks_fail_quorum_valid;

--bun:split

alter table checks add constraint checks_fail_quorum_valid check (
  fail_quorum is null
  or fail_quorum in ('all', 'majority')
  or (fail_quorum ~ '^[1-9][0-9]{0,2}$' and fail_quorum::integer <= 100)
);

--bun:split

comment on column checks.fail_quorum is
  'Multi-region quorum: how many of the check''s regions must be failing (for the confirmation period) before it is down. all | majority | a positive integer; NULL = all for 1-2 regions, majority for 3+. Spec 2026-09-25-10.';

--bun:split

create table if not exists check_region_states (
  check_uid         uuid not null references checks(uid) on delete cascade,
  region            text not null,
  organization_uid  uuid not null references organizations(uid) on delete cascade,
  status            smallint not null,
  status_since      timestamptz not null,
  last_result_at    timestamptz not null,
  updated_at        timestamptz not null default now(),
  primary key (check_uid, region)
);

--bun:split

comment on table check_region_states is
  'Newest real reading per (check, region) for checks with 2+ regions, read by the multi-region quorum. Rows for a region the check no longer runs in are ignored, not pruned. Spec 2026-09-25-10.';

--bun:split

comment on column check_region_states.status is
  'Result status of the region''s newest real result (3 up, 4 down, 5 timeout, 6 error, 8 warning).';

--bun:split

comment on column check_region_states.status_since is
  'When the region entered its current side (failing or passing). Display only.';

--bun:split

-- ==========================================================================
-- SECTION: auth-handoff-codes  (spec 2026-09-25-12)
--
-- A federated login (Google, GitHub, OIDC, SAML, ...) used to redirect the
-- browser to the dashboard with access_token / refresh_token in the query
-- string, where they reached browser history, Referer headers, proxy logs and
-- session replay. The callback now stores the minted session under a random,
-- single-use, 60-second handoff code and redirects with that code only; the
-- dashboard trades it once via POST /api/v1/auth/handoff/exchange.
--
--   code_hash         hex SHA-256 of the code. The code itself is never
--                     stored, so a row cannot be redeemed from a dump.
--   payload           the session, AES-256-GCM sealed under a key derived
--                     from the code (not from the hash): a dump yields no
--                     token either.
--   user_uid /        who the session was minted for; the row goes with the
--   organization_uid  user or org. organization_uid is NULL for an org-less
--                     (pending membership) session.
-- ==========================================================================

create table if not exists auth_handoff_codes (
  code_hash         text primary key,
  user_uid          uuid not null references users(uid) on delete cascade,
  organization_uid  uuid references organizations(uid) on delete cascade,
  payload           text not null,
  expires_at        timestamptz not null,
  created_at        timestamptz not null default now()
);

--bun:split

create index if not exists auth_handoff_codes_expires_at_idx on auth_handoff_codes (expires_at);

--bun:split

comment on table auth_handoff_codes is
  'Single-use, short-lived codes handing a federated login''s session to the dashboard (POST /api/v1/auth/handoff/exchange). Deleted on first use; expired rows are swept by the state-cleanup job. Spec 2026-09-25-12.';

--bun:split

comment on column auth_handoff_codes.code_hash is
  'Hex SHA-256 of the code. The code itself is never stored.';

--bun:split

comment on column auth_handoff_codes.payload is
  'The session (tokens, return path), AES-256-GCM sealed under a key derived from the code. Unreadable without the code.';
