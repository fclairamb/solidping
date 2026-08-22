-- v0.17.0 — the ONE consolidated migration for the (still unreleased) v0.17.0
-- release. 013_v0_16_0 is the last RELEASED migration, so everything this
-- cycle produced lands here, in a single file per dialect, per the repo
-- convention documented in wiki/conventions/database.md.
--
-- It is long because it is a whole release, not because anything here is
-- subtle. It is organised into SECTIONs, each one a scratch migration that was
-- folded in at consolidation time and each one preserving that migration's own
-- rationale verbatim:
--
--   SECTION: results-status-domain      abandoned-result reaper status + index
--   SECTION: remove-opsgenie-integrations  drop the sunsetting Opsgenie type
--   SECTION: worker-version             workers.version self-report column
--   SECTION: incident-publications      public incident overlay + auto-publish
--   SECTION: slo-reporting              SLOs, report schedules, maintenance tags
--   SECTION: dangling-notification-routes  delete routes whose contact is gone
--   SECTION: file-attachments          files.topic + files.details attachments
--
-- ORDER IS LOAD-BEARING. Sections run top to bottom and later ones build on
-- earlier ones (on SQLite in particular, results-status-domain REBUILDS
-- `results` before slo-reporting adds columns to it). The .down.sql unwinds
-- them in the exact reverse order.
--
-- The SECTION banners are also a machine-readable anchor: migration tests slice
-- one section out of this file so they can replay just that block against a
-- populated database. Renaming a section renames a test fixture — see
-- migrationSection() in the db test packages.
--
-- NOTE ON THE "015 + 016" MENTIONED IN THE FIRST SECTION BELOW: that is an
-- EARLIER, different pair (the short-lived `results.abandoned` flag), already
-- folded away before the sections below were even written. It is unrelated to
-- the 015-018 folded in here.
--
-- Consolidation caveat: a database that already applied 014 in an earlier,
-- shorter form has 014 recorded in bun_migrations, so the sections folded in
-- afterwards will be SKIPPED there and the migration guard will flag the
-- checksum drift on boot. Development databases must be RESET, not repaired.

-- ==========================================================================
-- SECTION: results-status-domain
-- The v0.17.0 file's original body (specs 2026-08-18-03 and 2026-08-18-10).
-- ==========================================================================

-- v0.17.0 — the abandoned-result reaper: a dedicated ResultStatusAbandoned (9)
-- status value plus the partial index the reaper scans on
-- (specs 2026-08-18-03 and 2026-08-18-10, consolidated).
--
-- WHY THE REAPER EXISTS
--
-- A raw result left in ResultStatusCreated never gets a matching terminal
-- write when the process that claimed it dies before finishing. The most
-- visible source today is CreateCheck's one-time "Check created" marker row:
-- the aggregation job deliberately never deletes a lifecycle-marker row (it
-- is "intentionally preserved" so a permalink never 404s — see
-- job_aggregation.go's measurableSourceUIDs), so once every later raw row
-- ages out of retention the marker becomes the newest raw row again and
-- "Last checked" reads it verbatim — a check that stopped reporting three
-- weeks ago showing "Last checked 20d ago" next to "Currently up for 2d 10h".
--
-- WHY ONLY `created` IS REAPED, NEVER `running`
--
-- models.ResultStatus.IsLifecycleMarker groups both as non-measurement
-- statuses for availability purposes, but `running` is also used by heartbeat
-- checks (handlers/heartbeat) as a legitimate, externally-reported, possibly
-- long-lived status with no check_jobs period/lease for a "plausible execution
-- window" to be measured against — reaping it would finalize a heartbeat's
-- genuine in-progress report into a fake error.
--
-- WHY A STATUS RATHER THAN A FLAG
--
-- The first cut encoded a reaped attempt as "status = error, plus an invisible
-- asterisk" (`abandoned = true`). The two axes never actually vary
-- independently — the reaper is the flag's only writer, the state is terminal,
-- and there is no "abandoned but some other status" combination — so the flag
-- collapses into one more case of the status enum it was shadowing. Rendering
-- a reaped attempt as `error` was also less honest than a status of its own:
-- everywhere else `error` means the attempt ran and failed, and a reader then
-- had to know about a second column to learn that this particular error is
-- quietly un-counted from availability.
--
-- Status 9 is excluded from availability math (successful_checks /
-- total_checks) everywhere it is computed — see
-- Result.ExcludedFromAvailability, the one predicate the hour rollup, the
-- uptimebar union and the availability service all route through so they
-- cannot drift apart. Counting a reaped attempt as downtime would manufacture
-- an outage out of OUR worker's crash, not the monitored service's.
--
-- WHY THE PARTIAL INDEX
--
-- It backs the reaper's own candidate scan: the reaper is looking for a
-- handful of stuck marker rows inside `results`, the largest table in the
-- system, and must never fall back to a sequential scan of it. It covers only
-- status=1 (created) — status=2 (running) is excluded on purpose (see above),
-- and heartbeat checks can write many long-lived running rows that this index
-- must not have to carry.
--
-- There is NO data conversion step here. A database applying this migration
-- has never had an `abandoned` column, so there is nothing to convert.

-- 1. Widen `status in (0..8)` to `(0..9)`. The constraint is an unnamed inline
--    column check from 001 (released and frozen, so it cannot be edited), so
--    Postgres named it itself; rather than betting on `results_status_check`,
--    drop whichever check constraint on `results` actually mentions `status`
--    (there is exactly one — the only other check on the table constrains
--    period_type).
do $$
declare
  con record;
begin
  for con in
    select c.conname
    from pg_constraint c
    join pg_class t on t.oid = c.conrelid
    join pg_namespace n on n.oid = t.relnamespace
    where t.relname = 'results'
      and n.nspname = current_schema()
      and c.contype = 'c'
      and pg_get_constraintdef(c.oid) ilike '%status%'
  loop
    execute format('alter table results drop constraint %I', con.conname);
  end loop;
end
$$;

--bun:split

-- Validating (not NOT VALID): the new domain is a strict superset of the one
-- just dropped, so the scan can only confirm what is already true, but it
-- keeps the constraint indistinguishable from a freshly-created schema's — the
-- release-time golden-schema diff compares them.
alter table results
  add constraint results_status_check check (status in (0, 1, 2, 3, 4, 5, 6, 7, 8, 9));

--bun:split

create index idx_results_lifecycle_pending on results (period_start)
  where period_type = 'raw' and status = 1;

--bun:split

-- 001's comment on this column has been wrong since the enum was renumbered;
-- refresh it now that 9 joins the domain.
comment on column results.status is
  '1=created, 2=running, 3=up, 4=down, 5=timeout, 6=error, 7=degraded (aggregated only), 8=warning, 9=abandoned (reaper-minted, excluded from availability).';

--bun:split

-- ==========================================================================
-- SECTION: remove-opsgenie-integrations
-- Was scratch migration 015_remove_opsgenie_integrations (spec 2026-08-19-02).
-- ==========================================================================

-- Remove the sunsetting Opsgenie integration type, replaced by PagerDuty
-- (spec 2026-08-19-02: Atlassian is retiring Opsgenie in April 2027). Hard
-- delete, not a tombstone: the product is pre-1.0 and keeping a dead
-- connection_type around would force every type switch in the code to carry
-- a zombie case forever.
--
-- FK graph around integrations(uid) (see 001_v0_1_0.up.sql):
--   - check_channels.integration_uid              ON DELETE CASCADE  (automatic)
--   - user_integration_identities.integration_uid  ON DELETE CASCADE  (automatic, 011_v0_14_0)
--   - incident_notifications.connection_uid        ON DELETE SET NULL (automatic — keeps audit history)
--   - escalation_policy_targets.target_uid          NOT a DB foreign key: it is a
--     polymorphic column shared by target_type in ('user','schedule','connection',
--     'all_admins'), so nothing enforces it. A 'connection' target pointing at an
--     opsgenie integration must be deleted explicitly here, or it becomes a
--     silently-dangling reference the moment the integration row is gone.

delete from escalation_policy_targets
where target_type = 'connection'
  and target_uid in (select uid from integrations where type = 'opsgenie');

--bun:split

delete from integrations where type = 'opsgenie';

--bun:split

-- ==========================================================================
-- SECTION: worker-version
-- Was scratch migration 016_worker_version (spec 2026-08-19-07).
-- ==========================================================================

-- Workers self-report their own build version (spec 2026-08-19-07), so fleet
-- version drift becomes visible instead of requiring a shell into every
-- deployment target. Mirrors the nullable/no-backfill discipline of the
-- `capabilities` column recipe in 013_v0_16_0.up.sql, adapted for a SCALAR
-- rather than a set:
--
--   NULL       unknown — nothing was ever reported (an agent predating this
--              feature, or a worker that has not sent a claim frame yet)
--   'x.y.z'    the version this worker last reported
--
-- Unlike `capabilities`, there is no meaningful "reported but empty" third
-- state for a single version string — a real build version is never the
-- empty string — so this is a two-state column, not three. NULL is still the
-- ONLY unknown, and it must never be rendered as "drifted": an old agent that
-- predates version reporting must not look broken. This is detection only —
-- nothing here blocks, throttles or disconnects an agent on a stale version.

alter table workers add column if not exists version text;

--bun:split

comment on column workers.version is
  'Self-reported build version (internal/version.Get().Version). NULL = never reported (unknown) — must never be rendered as "drifted". Never an empty string — see the CHECK.';

--bun:split

alter table workers drop constraint if exists workers_version_not_empty;

--bun:split

-- A plain scalar CHECK, unlike capabilities' element-shape regex: there is
-- exactly one value to validate here, not a set of them, so there is nothing
-- for a charset/duplicate rule to apply to. This only buys back "empty string
-- is not a version" — the one way a scalar column could silently relearn the
-- three-state confusion the capabilities migration exists to avoid.
alter table workers add constraint workers_version_not_empty check (
  version is null or version <> ''
);

--bun:split

-- ==========================================================================
-- SECTION: incident-publications
-- Was scratch migration 017_incident_publications (spec 2026-08-19-08).
-- ==========================================================================

-- Incident publications: the publication overlay that finally lets an
-- automatic incident reach a status page (spec 2026-08-19-08).
--
-- The operational `incidents` row stays exactly as it is. It carries ack /
-- snooze metadata, auto-generated internal titles ("api-eu is down") and probe
-- diagnostics in `details` — none of which may ever be shown to a customer.
-- This table is the thin, deliberately public-only overlay on top of it:
-- "incident X is visible on page Y, under this customer-readable title, in
-- this state". Nothing here is derived from the incident's diagnostics, and
-- there is no column a probe's output could ever be written into. That is the
-- security boundary of the feature, expressed in the schema rather than in a
-- code review comment.

create table if not exists incident_publications (
  uid                 uuid primary key default gen_random_uuid(),
  organization_uid    uuid not null references organizations(uid),
  incident_uid        uuid references incidents(uid),
  status_page_uid     uuid not null references status_pages(uid),
  public_title        text not null,
  public_state        text not null default 'investigating'
                        check (public_state in ('investigating', 'identified', 'monitoring', 'resolved')),
  severity            text check (severity is null or severity in ('minor', 'major', 'critical')),
  auto_created        boolean not null default false,
  human_touched_at    timestamptz,
  published_at        timestamptz not null default now(),
  resolved_at         timestamptz,
  notify_window_start timestamptz,
  notify_window_count integer not null default 0,
  created_at          timestamptz not null default now(),
  updated_at          timestamptz not null default now(),
  deleted_at          timestamptz
);

--bun:split

-- One publication per (incident, page). This index is what makes the debounce
-- job idempotent: a concurrent job fire and a manual publish race into the same
-- row, and the loser gets a unique violation it can recover from by re-reading,
-- rather than minting a duplicate public incident. Hand-authored publications
-- (incident_uid NULL) are exempt — an operator may post as many as they like.
create unique index if not exists uq_incident_publications_incident_page
  on incident_publications (incident_uid, status_page_uid)
  where incident_uid is not null and deleted_at is null;

--bun:split

create index if not exists idx_incident_publications_page_published
  on incident_publications (status_page_uid, published_at desc)
  where deleted_at is null;

--bun:split

create index if not exists idx_incident_publications_incident
  on incident_publications (incident_uid)
  where incident_uid is not null and deleted_at is null;

--bun:split

comment on table incident_publications is
  'Public-facing publication overlay for incidents (spec 2026-08-19-08). Every column is safe to render publicly except notify_window_*; probe output and error strings must never be written here.';

--bun:split

comment on column incident_publications.incident_uid is
  'Operational incident this publication tracks. NULL = hand-authored, tracks nothing.';

--bun:split

comment on column incident_publications.human_touched_at is
  'First manual edit / manual update. Drives the if_untouched auto-resolve policy.';

--bun:split

comment on column incident_publications.notify_window_count is
  'Subscriber fan-out waves sent inside the rolling hour starting at notify_window_start (storm cap). Internal bookkeeping, never public.';

--bun:split

-- Page-level auto-publish settings. auto_publish defaults to FALSE at the DDL
-- level ON PURPOSE: an existing installation upgrading to this release must not
-- discover that its internal blips are now public. New pages opt in through the
-- application's create path (models.NewStatusPage), so the migration stays
-- honest about existing rows.
alter table status_pages add column if not exists auto_publish boolean not null default false;

--bun:split

alter table status_pages add column if not exists auto_publish_delay_seconds integer not null default 60;

--bun:split

alter table status_pages add column if not exists auto_resolve text not null default 'if_untouched';

--bun:split

alter table status_pages drop constraint if exists status_pages_auto_resolve_valid;

--bun:split

alter table status_pages add constraint status_pages_auto_resolve_valid
  check (auto_resolve in ('always', 'if_untouched', 'never'));

--bun:split

alter table status_pages drop constraint if exists status_pages_auto_publish_delay_nonneg;

--bun:split

alter table status_pages add constraint status_pages_auto_publish_delay_nonneg
  check (auto_publish_delay_seconds >= 0 and auto_publish_delay_seconds <= 86400);

--bun:split

comment on column status_pages.auto_publish is
  'Auto-publish incidents affecting this page. FALSE for every pre-existing row by design; new pages default TRUE in the application create path.';

--bun:split

comment on column status_pages.auto_publish_delay_seconds is
  'Debounce before an incident is published. 0 publishes immediately; a shorter outage than this never reaches the public page.';

--bun:split

-- Three-state per-resource override: NULL = inherit the page. Making it
-- nullable rather than defaulting to the page value means flipping the page
-- setting does not silently rewrite every resource's intent.
alter table status_page_resources add column if not exists auto_publish boolean;

--bun:split

comment on column status_page_resources.auto_publish is
  'Per-resource auto-publish override. NULL = inherit the status page setting.';

--bun:split

-- Narrative rows stay in status_updates. Two changes are needed:
--   * a link to the publication, so a HAND-AUTHORED publication (which has no
--     incident_uid to thread on) still has a thread;
--   * author_uid becomes nullable, because an auto-generated update has no
--     author. Attributing a machine post to whichever human happens to own the
--     org would be a lie the UI then renders.
alter table status_updates add column if not exists incident_publication_uid uuid
  references incident_publications(uid);

--bun:split

alter table status_updates alter column author_uid drop not null;

--bun:split

create index if not exists idx_status_updates_publication
  on status_updates (incident_publication_uid)
  where incident_publication_uid is not null and deleted_at is null;

--bun:split

comment on column status_updates.author_uid is
  'User who posted the update. NULL = generated by the auto-publish pipeline (no human author).';

--bun:split

-- ==========================================================================
-- SECTION: slo-reporting
-- Was scratch migration 018_slo_reporting (spec 2026-08-20-01).
-- ==========================================================================

-- SLA/SLO reporting (spec 2026-08-20-01): an SLO object with a calendar-month
-- error budget, and scheduled uptime report digests.
--
-- Nothing per-window is stored: attainment, budget and history are always
-- computed at read time off the permanent `month` rollups. The only durable
-- artifact is the emailed report.

-- Maintenance tagging on raw results. This is ingest-time and therefore NOT
-- retroactive: every pre-existing row is honestly `false`, and the SLO status
-- API reports excludedMaintenanceSeconds explicitly so a partially-covered
-- month is legible rather than silently wrong.
alter table results add column if not exists maintenance boolean not null default false;

--bun:split

-- Aggregated counters, carried up hour -> day -> month next to total_checks /
-- successful_checks. Both are SUBSETS of those two, so every existing consumer
-- (status pages, badges, the availability API) is unchanged: raw availability
-- keeps counting maintenance probes. Only the SLO read path subtracts them.
alter table results add column if not exists maintenance_checks integer;

--bun:split

alter table results add column if not exists maintenance_successful_checks integer;

--bun:split

comment on column results.maintenance is
  'Raw rows only: an active maintenance window covered the check when this probe was recorded. Set at ingest, never backfilled.';

--bun:split

comment on column results.maintenance_checks is
  'Aggregated rows only: how many of total_checks were recorded under maintenance. Subset of total_checks.';

--bun:split

comment on column results.maintenance_successful_checks is
  'Aggregated rows only: how many of successful_checks were recorded under maintenance. Subset of both successful_checks and maintenance_checks.';

--bun:split

create table if not exists slos (
  uid                 uuid primary key default gen_random_uuid(),
  organization_uid    uuid not null references organizations(uid),
  name                text not null,
  slug                text not null,
  check_uid           uuid references checks(uid) on delete cascade,
  check_group_uid     uuid references check_groups(uid) on delete cascade,
  target_pct          numeric(6,3) not null,
  timezone            text not null default 'UTC',
  exclude_maintenance boolean not null default true,
  enabled             boolean not null default true,
  created_at          timestamptz not null default now(),
  updated_at          timestamptz not null default now(),
  deleted_at          timestamptz,
  -- Exactly one scope. A "both" SLO would have two different denominators and
  -- no defensible answer; a "neither" SLO would silently mean the whole org,
  -- which is a different feature. The schema refuses both rather than leaving
  -- it to whichever code path happens to read the row.
  constraint slos_scope_xor check (
    (check_uid is not null and check_group_uid is null)
    or (check_uid is null and check_group_uid is not null)
  ),
  constraint slos_target_pct_range check (target_pct > 0 and target_pct <= 100)
);

--bun:split

create unique index if not exists uq_slos_org_slug
  on slos (organization_uid, slug)
  where deleted_at is null;

--bun:split

create index if not exists idx_slos_org_enabled
  on slos (organization_uid, enabled)
  where deleted_at is null;

--bun:split

create index if not exists idx_slos_check
  on slos (check_uid)
  where check_uid is not null and deleted_at is null;

--bun:split

create index if not exists idx_slos_check_group
  on slos (check_group_uid)
  where check_group_uid is not null and deleted_at is null;

--bun:split

comment on table slos is
  'Service-level objectives (spec 2026-08-20-01). Windows are calendar months in `timezone`; nothing per-window is stored.';

--bun:split

comment on column slos.exclude_maintenance is
  'Subtract probes tagged results.maintenance from the SLO denominator. Only affects this SLO — status pages and badges keep showing raw availability.';

--bun:split

-- Scheduled uptime report digests. Deliberately NOT a column on `slos`: a
-- digest is useful for checks that carry no formal objective at all.
create table if not exists report_schedules (
  uid                   uuid primary key default gen_random_uuid(),
  organization_uid      uuid not null references organizations(uid),
  name                  text not null,
  frequency             text not null default 'monthly'
                          check (frequency in ('weekly', 'monthly')),
  timezone              text not null default 'UTC',
  -- PII. Same handling bar as status_page_subscribers.email: never echoed into
  -- events, never logged, only ever read back to the org's own admins.
  recipients            jsonb,
  check_uids            jsonb,
  check_group_uids      jsonb,
  include_slos          boolean not null default true,
  enabled               boolean not null default true,
  -- Start of the last period this schedule was actually reported for, in UTC.
  -- This is the duplicate-run suppression key: a second job fire for the same
  -- closed period is a no-op, so multi-replica claiming needs no leader.
  last_period_start     timestamptz,
  last_run_at           timestamptz,
  created_at            timestamptz not null default now(),
  updated_at            timestamptz not null default now(),
  deleted_at            timestamptz
);

--bun:split

create index if not exists idx_report_schedules_org_enabled
  on report_schedules (organization_uid, enabled)
  where deleted_at is null;

--bun:split

comment on table report_schedules is
  'Scheduled uptime report digests (spec 2026-08-20-01). Empty check_uids AND check_group_uids means org-wide.';

--bun:split

comment on column report_schedules.recipients is
  'JSON array of email addresses. PII — never log, never emit into events.';

--bun:split

comment on column report_schedules.last_period_start is
  'UTC start of the last reported period. Duplicate-run suppression key for JobTypeUptimeReport.';

--bun:split

-- ==========================================================================
-- SECTION: dangling-notification-routes
-- Was scratch migration 019_dangling_notification_routes (spec 2026-08-20-02).
-- ==========================================================================

-- Clean up notification routes whose contact is gone (spec 2026-08-20-02).
--
-- `user_notification_routes.contact_uid` carries an `on delete cascade` FK, but
-- a cascade only fires on a HARD delete. Every contact deletion in this system
-- is a SOFT delete (the dashboard's "remove method", the webpush prune on a
-- 404/410 Gone, the Telegram /stop webhook), so each one left its route row
-- behind. The list query then returned that route with all-NULL contact columns
-- and the dashboard rendered an undeletable ghost row: no title, no value, and
-- a delete request keyed on an empty contact uid that can never match.
--
-- The code no longer creates these (DeleteUserContact deletes the route in the
-- same transaction, and the list query requires a joined contact). This clears
-- the ones already in the wild.
--
-- Hard delete, no tombstone: `user_notification_routes` has no deleted_at
-- column by design — a route is a pure join row, and one whose contact is gone
-- carries no information worth keeping.
--
-- `not exists` rather than a `left join ... is null`: it reads as the sentence
-- the fix actually is ("no live contact backs this route") and is idempotent,
-- so re-running the section on an already-clean database is a no-op. The
-- migration tests replay just this block for exactly that reason.
delete from user_notification_routes r
where not exists (
  select 1 from user_contacts c
  where c.uid = r.contact_uid
    and c.deleted_at is null
);

--bun:split

-- ==========================================================================
-- SECTION: file-attachments
-- Generic file attachments: files.topic + files.details (spec 2026-08-21-01).
-- ==========================================================================

-- `topic` is what turns a `files` row from "a blob somebody uploaded" into "an
-- attachment OF something". It is a path-like key, `<entity>/<uid>/<kind>`,
-- e.g. `incidents/9a1eb273-.../screenshot`.
--
-- NULL for every file that is not an attachment — org logos and bug-report
-- screenshots keep working untouched, which is why the column is nullable and
-- has no default.
--
-- A path rather than a (entity_type, entity_uid, kind) triple on purpose: the
-- reaping story is "delete everything under this prefix", which a single
-- LIKE-anchored column answers directly, and a new attachment kind is a new
-- topic string rather than a new column or a new enum value.
alter table files add column topic text;

-- Free metadata bag, same shape as every other jsonb column here. For a
-- screenshot: {"capturedAt": ..., "region": ..., "checkUid": ..., "trigger": ...}.
alter table files add column details jsonb;

comment on column files.topic is
  'Attachment key, <entity>/<uid>/<kind>. NULL for non-attachment files (org logos, bug reports).';
comment on column files.details is
  'Free metadata bag for an attachment (capture timestamp, region, originating check, ...).';

-- Partial: attachments are a small minority of `files` rows, and every query
-- against this column is an org-scoped, live-rows-only attachment lookup.
create index files_org_topic_idx on files (organization_uid, topic)
  where deleted_at is null and topic is not null;
