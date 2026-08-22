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

-- SQLite mirror of the results-status-domain section of
-- postgres/migrations/014_v0_17_0.up.sql — the
-- abandoned-result reaper: the dedicated ResultStatusAbandoned (9) status
-- value plus the partial index the reaper scans on (specs 2026-08-18-03 and
-- 2026-08-18-10, consolidated). See the Postgres file for why the reaper
-- exists, why only status=1 (created) is reaped and never status=2 (running,
-- used by heartbeat checks for a legitimate long-lived report), why the state
-- is a status rather than a boolean flag alongside `error`, why a reaped
-- attempt is excluded from availability, and why this is numbered 014.
--
-- Postgres widens the CHECK domain with a DROP CONSTRAINT / ADD CONSTRAINT
-- pair. SQLite has neither DROP CONSTRAINT nor ALTER COLUMN, and 001 declares
-- `status integer check (status in (0, ..., 8))` inline, so the table has to be
-- rebuilt with the established *_new pattern (same technique as 005_v0_4_0 and
-- 009_v0_8_0).
--
-- There is no data conversion in the rebuild: no database applying this
-- migration ever had an `abandoned` column. The column list is still spelled
-- out explicitly rather than `select *` — `insert ... select` is positional,
-- and a silent column-order drift here would scramble every row of the largest
-- table in the system.
--
-- Nothing has a foreign key TO `results`, so dropping it fires no cascade into
-- a live table. foreign_keys is still switched off around the rebuild because
-- `results` has keys OUT to organizations/checks/workers and legacy databases
-- can carry rows whose check was hard-deleted underneath them (the case
-- ReapAbandonedResults explicitly tolerates); re-inserting those with the
-- constraint on would fail the migration rather than carry the data across.
-- The PRAGMAs sit in their own statements so they run in autocommit — a
-- PRAGMA foreign_keys inside a transaction is silently a no-op.

PRAGMA foreign_keys=OFF;

--bun:split

create table results_new (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade, -- Owning organization
  check_uid         text not null references checks(uid) on delete cascade, -- Check that produced this result
  period_type       text not null default 'raw' check (period_type in ('raw', 'hour', 'day', 'month', 'year')), -- Granularity: raw or aggregated
  period_start      text not null, -- Execution timestamp (raw) or aggregation period start
  period_end        text, -- Aggregation period end. NULL for raw results
  region            text, -- Region where the check was executed

  -- Raw result fields (period_type = 'raw')
  worker_uid        text references workers(uid) on delete set null, -- Worker that executed this check (raw only)
  status            integer check (status in (0, 1, 2, 3, 4, 5, 6, 7, 8, 9)), -- 1=created, 2=running, 3=up, 4=down, 5=timeout, 6=error, 7=degraded (aggregated), 8=warning, 9=abandoned (reaper-minted, excluded from availability)
  duration          real, -- Total check duration in milliseconds (raw only)
  metrics           text, -- Numerical metrics: ttfb, dnsTime, tlsHandshake, etc. (raw only)
  output            text, -- Diagnostic output: error messages, HTTP status, headers (raw only)

  -- Aggregated fields (period_type = 'hour', 'day', 'month', 'year')
  total_checks      integer, -- Number of check executions in this period
  successful_checks integer, -- Number of successful executions in this period
  duration_min      real, -- Minimum duration in this period
  duration_max      real, -- Maximum duration in this period
  duration_p95      real, -- 95th percentile duration in this period
  duration_avg      real, -- Average duration in this period

  created_at        text not null default (datetime('now'))
);

--bun:split

insert into results_new (
  uid, organization_uid, check_uid, period_type, period_start, period_end, region,
  worker_uid, status, duration, metrics, output,
  total_checks, successful_checks, duration_min, duration_max, duration_p95, duration_avg,
  created_at
)
select
  uid, organization_uid, check_uid, period_type, period_start, period_end, region,
  worker_uid, status, duration, metrics, output,
  total_checks, successful_checks, duration_min, duration_max, duration_p95, duration_avg,
  created_at
from results;

--bun:split

drop table results;

--bun:split

alter table results_new rename to results;

--bun:split

create index results_raw_idx on results (organization_uid, check_uid, period_start desc) where period_type = 'raw';

--bun:split

create index results_aggregated_idx on results (organization_uid, check_uid, period_type, period_start desc) where period_type != 'raw';

--bun:split

-- NOT 001's definition: migration 006 replaced it with this NULL-proof form
-- over coalesce(region, '') to close the aggregation poison-pill loop that
-- duplicated `hour` rows unbounded (spec 2026-07-11-16). SQLite treats every
-- NULL as distinct in a unique index, so the bare `region` form silently
-- stops constraining region-less rollups at all. Copied verbatim from
-- 006_v0_5_0.up.sql — a rebuild that recreates indexes from the ORIGINAL
-- create-table migration reopens every index fix shipped since.
create unique index results_aggregated_unique_idx
  on results (organization_uid, check_uid, coalesce(region, ''), period_type, period_start)
  where period_type != 'raw';

--bun:split

-- The reaper's candidate index: status=1 (created) only, never status=2.
create index idx_results_lifecycle_pending on results (period_start)
  where period_type = 'raw' and status = 1;

--bun:split

PRAGMA foreign_keys=ON;

--bun:split

-- ==========================================================================
-- SECTION: remove-opsgenie-integrations
-- Was scratch migration 015_remove_opsgenie_integrations (spec 2026-08-19-02).
-- ==========================================================================

-- SQLite mirror of the remove-opsgenie-integrations section of
-- postgres/migrations/014_v0_17_0.up.sql
-- — remove the sunsetting Opsgenie integration type, replaced by PagerDuty
-- (spec 2026-08-19-02: Atlassian is retiring Opsgenie in April 2027). Hard
-- delete, not a tombstone: the product is pre-1.0 and keeping a dead
-- connection_type around would force every type switch in the code to carry
-- a zombie case forever.
--
-- See the Postgres file for the FK graph this deletion order
-- relies on:
--   - check_channels.integration_uid              ON DELETE CASCADE  (automatic)
--   - user_integration_identities.integration_uid  ON DELETE CASCADE  (automatic, 011_v0_14_0)
--   - incident_notifications.connection_uid        ON DELETE SET NULL (automatic — keeps audit history)
--   - escalation_policy_targets.target_uid          NOT a foreign key (polymorphic
--     column shared by every target_type), so a 'connection' target pointing at an
--     opsgenie integration is deleted explicitly below.
--
-- No PRAGMA foreign_keys toggling needed: this only deletes rows, it does not
-- rebuild any table.

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

-- SQLite mirror of the worker-version section of
-- postgres/migrations/014_v0_17_0.up.sql — workers
-- self-report their own build version (spec 2026-08-19-07). See the Postgres
-- file for the full rationale: NULL-only-unknown, no backfill, and why this
-- is a two-state column (scalar) rather than three-state (set) the way
-- `capabilities` is.
--
-- SQLite has no `ADD COLUMN IF NOT EXISTS`, so this is not re-runnable —
-- same as every other scratch migration this cycle. No triggers are needed
-- here (unlike `capabilities`): a plain column CHECK is enough to validate a
-- single scalar, where the array-element rules for `capabilities` needed
-- `json_each` triggers because SQLite forbids subqueries in a CHECK and
-- json_each is table-valued.

alter table workers add column version text
  check (version is null or version <> '');

--bun:split

-- ==========================================================================
-- SECTION: incident-publications
-- Was scratch migration 017_incident_publications (spec 2026-08-19-08).
-- ==========================================================================

-- SQLite mirror of the incident-publications section of
-- postgres/migrations/014_v0_17_0.up.sql — the
-- incident publication overlay (spec 2026-08-19-08). See the Postgres file for
-- the full rationale, in particular why nothing derived from an incident's
-- probe diagnostics has a column to land in here.
--
-- SQLite has no ADD COLUMN IF NOT EXISTS, so this file is not re-runnable —
-- same as every other scratch migration this cycle.

create table if not exists incident_publications (
  uid                 text primary key,
  organization_uid    text not null references organizations(uid),
  incident_uid        text references incidents(uid),
  status_page_uid     text not null references status_pages(uid),
  public_title        text not null,
  public_state        text not null default 'investigating'
                        check (public_state in ('investigating', 'identified', 'monitoring', 'resolved')),
  severity            text check (severity is null or severity in ('minor', 'major', 'critical')),
  auto_created        integer not null default 0,
  human_touched_at    text,
  published_at        text not null default (datetime('now')),
  resolved_at         text,
  notify_window_start text,
  notify_window_count integer not null default 0,
  created_at          text not null default (datetime('now')),
  updated_at          text not null default (datetime('now')),
  deleted_at          text
);

--bun:split

-- Idempotency for the debounce job: a concurrent job fire and a manual publish
-- race into the same row rather than minting two public incidents.
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

-- FALSE for every pre-existing page by design; new pages opt in through
-- models.NewStatusPage. See the Postgres file.
alter table status_pages add column auto_publish integer not null default 0;

--bun:split

alter table status_pages add column auto_publish_delay_seconds integer not null default 60
  check (auto_publish_delay_seconds >= 0 and auto_publish_delay_seconds <= 86400);

--bun:split

alter table status_pages add column auto_resolve text not null default 'if_untouched'
  check (auto_resolve in ('always', 'if_untouched', 'never'));

--bun:split

-- NULL = inherit the page.
alter table status_page_resources add column auto_publish integer;

--bun:split

-- status_updates needs BOTH a publication link (so a hand-authored publication,
-- which has no incident_uid to thread on, still has a thread) and a nullable
-- author_uid: an auto-generated update has no author, and
-- attributing a machine post to a human would be a lie the UI then renders.
-- SQLite cannot relax NOT NULL in place, so status_updates is rebuilt with the
-- same *_new technique 002/005/009 used. foreign_keys is disabled around the
-- rebuild (in autocommit, isolated with --bun:split, since a PRAGMA inside a
-- transaction is silently a no-op) so nothing else loses its references.
PRAGMA foreign_keys=OFF;

--bun:split

create table status_updates_new (
  uid                      text primary key,
  organization_uid         text not null references organizations(uid),
  status_page_uid          text not null references status_pages(uid),
  section_uid              text references status_page_sections(uid),
  check_uid                text references checks(uid),
  incident_uid             text references incidents(uid),
  incident_publication_uid text references incident_publications(uid),
  title                    text not null,
  body_markdown            text not null,
  link_url                 text,
  kind                     text not null check (kind in ('investigating', 'identified', 'monitoring', 'resolved', 'maintenance', 'info')),
  published_at             text not null default (datetime('now')),
  author_uid               text references users(uid),
  created_at               text not null default (datetime('now')),
  updated_at               text not null default (datetime('now')),
  deleted_at               text
);

insert into status_updates_new (
  uid, organization_uid, status_page_uid, section_uid, check_uid, incident_uid,
  title, body_markdown, link_url, kind, published_at,
  author_uid, created_at, updated_at, deleted_at
)
select
  uid, organization_uid, status_page_uid, section_uid, check_uid, incident_uid,
  title, body_markdown, link_url, kind, published_at,
  author_uid, created_at, updated_at, deleted_at
from status_updates;

drop table status_updates;
alter table status_updates_new rename to status_updates;

create index idx_status_updates_org_page_pub on status_updates (organization_uid, status_page_uid, published_at desc);
create index idx_status_updates_incident on status_updates (incident_uid);
create index idx_status_updates_check on status_updates (check_uid);
create index idx_status_updates_publication on status_updates (incident_publication_uid);

--bun:split

PRAGMA foreign_keys=ON;

--bun:split

-- ==========================================================================
-- SECTION: slo-reporting
-- Was scratch migration 018_slo_reporting (spec 2026-08-20-01).
-- ==========================================================================

-- SQLite mirror of the slo-reporting section of
-- postgres/migrations/014_v0_17_0.up.sql — SLA/SLO
-- reporting (spec 2026-08-20-01). See the Postgres file for the full rationale,
-- in particular why the maintenance counters are subsets of the existing ones
-- and therefore change no existing consumer.
--
-- SQLite has no ADD COLUMN IF NOT EXISTS, so this file is not re-runnable —
-- same as every other scratch migration this cycle.

alter table results add column maintenance integer not null default 0;

--bun:split

alter table results add column maintenance_checks integer;

--bun:split

alter table results add column maintenance_successful_checks integer;

--bun:split

create table if not exists slos (
  uid                 text primary key,
  organization_uid    text not null references organizations(uid),
  name                text not null,
  slug                text not null,
  check_uid           text references checks(uid) on delete cascade,
  check_group_uid     text references check_groups(uid) on delete cascade,
  target_pct          real not null,
  timezone            text not null default 'UTC',
  exclude_maintenance integer not null default 1,
  enabled             integer not null default 1,
  created_at          text not null default (datetime('now')),
  updated_at          text not null default (datetime('now')),
  deleted_at          text,
  -- Exactly one scope; see the Postgres file.
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

-- recipients / check_uids / check_group_uids are JSON arrays stored as text.
-- recipients is PII — never log it, never emit it into events.
create table if not exists report_schedules (
  uid               text primary key,
  organization_uid  text not null references organizations(uid),
  name              text not null,
  frequency         text not null default 'monthly'
                      check (frequency in ('weekly', 'monthly')),
  timezone          text not null default 'UTC',
  recipients        text,
  check_uids        text,
  check_group_uids  text,
  include_slos      integer not null default 1,
  enabled           integer not null default 1,
  last_period_start text,
  last_run_at       text,
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text
);

--bun:split

create index if not exists idx_report_schedules_org_enabled
  on report_schedules (organization_uid, enabled)
  where deleted_at is null;

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
--
-- No table alias on the DELETE target: SQLite does not accept one, so the
-- correlated subquery names the table in full. Postgres uses `... r` there.
delete from user_notification_routes
where not exists (
  select 1 from user_contacts c
  where c.uid = user_notification_routes.contact_uid
    and c.deleted_at is null
);

--bun:split

-- ==========================================================================
-- SECTION: file-attachments
-- Generic file attachments: files.topic + files.details (spec 2026-08-21-01).
-- ==========================================================================

-- See the postgres half for the full rationale. Two dialect notes:
--   * `details` is TEXT here, like every other jsonb column in this schema —
--     bun marshals the same Go type into both.
--   * there is no `comment on column` in SQLite, so the documentation lives
--     only in these comments.
alter table files add column topic text;
alter table files add column details text;

create index files_org_topic_idx on files (organization_uid, topic)
  where deleted_at is null and topic is not null;
