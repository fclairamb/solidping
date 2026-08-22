-- v0.18.0 — SQLite mirror of the ONE consolidated migration for the (still
-- unreleased) v0.18.0 release. 014_v0_17_0 is the last RELEASED migration (tag
-- v0.17.0), so everything this cycle produces lands here, in a single file per
-- dialect, per the repo convention documented in wiki/conventions/database.md.
--
-- It is organised into SECTIONs mirroring postgres/migrations/015_v0_18_0.up.sql
-- one for one, each one preserving that section's own rationale:
--
--   SECTION: generic-attachments        files.topic/details attachment link
--   SECTION: status-page-branding       org-logo topic + public-route backfill
--   SECTION: status-page-password       status_pages password_hash
--   SECTION: status-subscriber-channels status_page_subscriber webhook/slack
--   SECTION: slo-burn-alerts           slo_alert_policies + incidents.kind/slo binding
--   SECTION: audit-actor-metadata      events source_ip/user_agent + wider actor_type
--
-- ORDER IS LOAD-BEARING. Sections run top to bottom and later ones build on
-- earlier ones. The .down.sql unwinds them in the exact reverse order.
--
-- The SECTION banners are also a machine-readable anchor: migration tests slice
-- one section out of this file so they can replay just that block against a
-- populated database. Renaming a section renames a test fixture — see
-- migrationSection() in the db test packages.

-- ==========================================================================
-- SECTION: generic-attachments
-- Was scratch migration 020_generic_attachments (spec 2026-08-21-01). Written
-- against 014_v0_17_0 on the stale premise that v0.17.0 was unreleased; moved
-- here verbatim once the tag proved it had shipped — a released migration is
-- never modified, because a database that already ran it never re-runs it.
-- ==========================================================================

-- SQLite mirror of the generic-attachments section of
-- postgres/migrations/015_v0_18_0.up.sql — a `files` row can now say what it
-- is attached to (spec 2026-08-21-01). See the Postgres file for the full
-- rationale: why the key is path-like, why NULL is the norm, why the index is
-- partial, and why an attachment must never reach a public surface.
--
-- Dialect differences, both cosmetic:
--   * `details` is TEXT here, like every other jsonb column in the SQLite
--     schema — models.JSONMap marshals to a string either way.
--   * SQLite has no `ADD COLUMN IF NOT EXISTS`, so these two statements are
--     not re-runnable — same as every other section in the SQLite set.
alter table files add column topic text;

--bun:split

alter table files add column details text;

--bun:split

create index if not exists files_org_topic_idx
  on files (organization_uid, topic)
  where deleted_at is null and topic is not null;

-- ==========================================================================
-- SECTION: status-page-branding
-- Brand assets: where they are stored, and how they are authorized
-- (specs 2026-08-21-07, 2026-08-22-03).
-- ==========================================================================

-- SQLite mirror of the status-page-branding section of
-- postgres/migrations/015_v0_18_0.up.sql. See the Postgres file for why the
-- three branding knobs are NOT columns (they live in `status_pages.settings`
-- under a `branding` key) and why the two partial indexes went with them.
--
-- Dialect difference: SQLite has no `UPDATE ... FROM`, so the org-logo topic
-- backfill uses a correlated subquery.
--
-- `o.deleted_at is null` is a SECURITY filter — see the Postgres file for why
-- a soft-deleted org's logo must NOT be granted a public topic. It is repeated
-- in both the SET subquery and the WHERE so the two can never select different
-- rows.

update files
   set topic = 'organizations/'
            || (select o.uid from organizations o
                 where o.logo_file_uid = files.uid and o.deleted_at is null)
            || '/logo'
 where topic is null
   and exists (select 1 from organizations o
                where o.logo_file_uid = files.uid and o.deleted_at is null);

--bun:split

update organizations
   set logo_url = '/pub/assets/' || logo_file_uid
 where logo_file_uid is not null
   and deleted_at is null
   and logo_url like '/pub/org-logos/%';

-- ==========================================================================
-- SECTION: status-page-password
-- Password-protected status pages (spec 2026-08-21-07).
-- ==========================================================================

-- SQLite mirror of the status-page-password section of
-- postgres/migrations/015_v0_18_0.up.sql. See the Postgres file for why the
-- hash doubles as the unlock cookie's HMAC key.
--
-- Unlike Postgres, SQLite cannot drop a CHECK constraint, and
-- `visibility text not null default 'public' check (visibility in
-- ('public','private'))` has been baked into this table since 001. Admitting a
-- third value therefore means the established *_new rebuild pattern (same
-- technique and same FK rationale as 009_v0_8_0.up.sql's rebuild of this very
-- table): status_pages is FK-referenced by status_page_sections,
-- status_page_subscriber and the incident publications, and with
-- foreign_keys=ON a DROP TABLE on a referenced parent fires its children's ON
-- DELETE actions against the still-live child rows BEFORE the rebuilt table is
-- swapped back in.
--
-- The PRAGMA statements are isolated with --bun:split so they execute on the
-- migration connection in autocommit — a PRAGMA foreign_keys issued inside a
-- transaction is silently a no-op.
--
-- INSERT column lists are spelled out explicitly (never `select *`):
-- `insert ... select` is positional, and a silent column-order drift here
-- would scramble every existing status page.
--
-- password_hash is added as part of the rebuild rather than by a separate
-- ALTER, so this section is one atomic shape change instead of two.

PRAGMA foreign_keys=OFF;

--bun:split

create table status_pages_new (
  uid                        text primary key,
  organization_uid           text not null references organizations(uid) on delete cascade, -- Owning organization
  name                       text not null, -- Page title displayed to visitors
  slug                       text not null check (slug is null or (length(slug) >= 3 and length(slug) <= 100)), -- URL-friendly identifier, unique per organization
  description                text, -- Subtitle or description shown on the page
  -- Access control: public (anyone), private (hidden entirely), password
  -- (shared with a secret — the public endpoints answer 401 until unlocked).
  visibility                 text not null default 'public' check (visibility in ('public', 'private', 'password')),
  is_default                 integer not null default 0, -- At most one default page per org
  enabled                    integer not null default 1, -- Whether the page is accessible
  show_availability          integer not null default 1, -- Whether to display uptime percentage
  show_response_time         integer not null default 1, -- Whether to display response time charts
  history_days               integer not null default 90, -- Number of days of history to display
  language                   text, -- ISO language code (e.g., en, fr). NULL uses system default
  created_at                 text not null default (datetime('now')),
  updated_at                 text not null default (datetime('now')),
  deleted_at                 text,
  history_period             text not null default '90d',
  custom_domain              text,
  custom_domain_token        text,
  custom_domain_verified_at  text,
  custom_domain_checked_at   text,
  custom_domain_failures     integer not null default 0,
  custom_css                 text,
  settings                   text not null default '{}',
  auto_publish               integer not null default 0,
  auto_publish_delay_seconds integer not null default 60
    check (auto_publish_delay_seconds >= 0 and auto_publish_delay_seconds <= 86400),
  auto_resolve               text not null default 'if_untouched'
    check (auto_resolve in ('always', 'if_untouched', 'never')),
  password_hash              text
);

--bun:split

insert into status_pages_new (
  uid, organization_uid, name, slug, description, visibility, is_default, enabled,
  show_availability, show_response_time, history_days, language, created_at, updated_at,
  deleted_at, history_period, custom_domain, custom_domain_token, custom_domain_verified_at,
  custom_domain_checked_at, custom_domain_failures, custom_css, settings,
  auto_publish, auto_publish_delay_seconds, auto_resolve
)
select
  uid, organization_uid, name, slug, description, visibility, is_default, enabled,
  show_availability, show_response_time, history_days, language, created_at, updated_at,
  deleted_at, history_period, custom_domain, custom_domain_token, custom_domain_verified_at,
  custom_domain_checked_at, custom_domain_failures, custom_css, settings,
  auto_publish, auto_publish_delay_seconds, auto_resolve
from status_pages;

--bun:split

drop table status_pages;

--bun:split

alter table status_pages_new rename to status_pages;

--bun:split

-- Every index the dropped table carried, recreated verbatim.
create unique index status_pages_org_slug_idx on status_pages (organization_uid, slug) where deleted_at is null;

--bun:split

create unique index status_pages_org_default_idx on status_pages (organization_uid) where is_default = 1 and deleted_at is null;

--bun:split

create unique index status_pages_custom_domain_idx
  on status_pages (custom_domain)
  where custom_domain is not null and deleted_at is null;

--bun:split

PRAGMA foreign_keys=ON;

-- ==========================================================================
-- SECTION: status-subscriber-channels
-- Webhook and Slack status-page subscriptions (spec 2026-08-21-07).
-- ==========================================================================

-- SQLite mirror of the status-subscriber-channels section of
-- postgres/migrations/015_v0_18_0.up.sql. See the Postgres file for why the
-- endpoint URL never lands in a readable column, what `endpoint_key` is for,
-- and why the failure counter exists.
--
-- Dialect difference: SQLite cannot drop a NOT NULL, so making `email`
-- nullable needs the *_new rebuild pattern. This table is referenced by
-- nothing, so the rebuild is simpler than the status_pages one above — but the
-- foreign_keys PRAGMA is still toggled, because status_page_subscriber itself
-- references organizations/status_pages/incidents and a rebuild under
-- foreign_keys=ON would re-validate every one of those on insert.

PRAGMA foreign_keys=OFF;

--bun:split

create table status_page_subscriber_new (
  uid               text primary key,
  organization_uid  text not null references organizations(uid),
  status_page_uid   text not null references status_pages(uid),
  -- Nullable now: a webhook or Slack subscriber has no email address.
  email             text,
  confirmed_at      text,
  confirm_token     text not null,
  unsubscribe_token text not null,
  scope             text not null check (scope in ('page', 'incident')),
  incident_uid      text references incidents(uid),
  created_at        text not null default (datetime('now')),
  deleted_at        text,
  channel           text not null default 'email' check (channel in ('email', 'webhook', 'slack')),
  endpoint_private  text,
  endpoint_hint     text,
  endpoint_key      text,
  failure_count     integer not null default 0,
  disabled_at       text
);

--bun:split

insert into status_page_subscriber_new (
  uid, organization_uid, status_page_uid, email, confirmed_at, confirm_token,
  unsubscribe_token, scope, incident_uid, created_at, deleted_at
)
select
  uid, organization_uid, status_page_uid, email, confirmed_at, confirm_token,
  unsubscribe_token, scope, incident_uid, created_at, deleted_at
from status_page_subscriber;

--bun:split

drop table status_page_subscriber;

--bun:split

alter table status_page_subscriber_new rename to status_page_subscriber;

--bun:split

create unique index idx_status_page_subscriber_confirm_token on status_page_subscriber (confirm_token);

--bun:split

create unique index idx_status_page_subscriber_unsub_token on status_page_subscriber (unsubscribe_token);

--bun:split

create index idx_status_page_subscriber_page_confirmed on status_page_subscriber (status_page_uid, confirmed_at);

--bun:split

-- Channel-aware live-uniqueness index: the pre-existing one keyed on
-- (page, email, scope, incident) and would collide every webhook row against
-- every other one now that `email` is null for them.
create unique index idx_status_page_subscriber_live
  on status_page_subscriber (
    status_page_uid, channel, coalesce(email, ''), coalesce(endpoint_key, ''),
    scope, coalesce(incident_uid, '')
  )
  where deleted_at is null;

--bun:split

PRAGMA foreign_keys=ON;

-- ==========================================================================
-- SECTION: slo-burn-alerts
-- Multiwindow burn-rate alerting for SLOs (spec 2026-08-21-08).
-- ==========================================================================

-- SQLite mirror of the slo-burn-alerts section of
-- postgres/migrations/015_v0_18_0.up.sql. See the Postgres file for why the
-- thresholds and windows are stored rather than derived, why `enabled`
-- defaults to false, and what `below_threshold_since` / `min_samples` are for.
--
-- Dialect differences: no gen_random_uuid() (the Go layer supplies the UID),
-- numeric -> real, timestamptz -> text, boolean -> integer.
create table if not exists slo_alert_policies (
  uid                   text primary key,
  organization_uid      text not null references organizations(uid),
  slo_uid               text not null references slos(uid) on delete cascade,
  kind                  text not null,
  enabled               integer not null default 0,
  long_window_seconds   integer not null,
  short_window_seconds  integer not null,
  threshold             real not null,
  severity              text not null,
  min_samples           integer not null default 3,
  last_evaluated_at     text,
  last_long_burn_rate   real,
  last_short_burn_rate  real,
  below_threshold_since text,
  created_at            text not null default (datetime('now')),
  updated_at            text not null default (datetime('now')),
  constraint slo_alert_policies_kind_check check (kind in ('fast', 'slow')),
  constraint slo_alert_policies_severity_check check (severity in ('critical', 'warning')),
  constraint slo_alert_policies_windows_check check (
    long_window_seconds > 0
    and short_window_seconds > 0
    and short_window_seconds <= long_window_seconds
  ),
  constraint slo_alert_policies_threshold_check check (threshold > 0),
  constraint slo_alert_policies_min_samples_check check (min_samples >= 1)
);

--bun:split

create unique index if not exists uq_slo_alert_policies_slo_kind
  on slo_alert_policies (slo_uid, kind);

--bun:split

create index if not exists idx_slo_alert_policies_enabled
  on slo_alert_policies (enabled)
  where enabled = 1;

--bun:split

-- `kind` discriminates a burn alert from a failing check. 'check' is the
-- pre-existing meaning and the default, so no backfill is needed.
--
-- SQLite allows ALTER TABLE ADD COLUMN with a REFERENCES clause only when the
-- default is NULL, which is exactly the case for the two binding columns — so
-- the incidents table does NOT need the *_new rebuild dance here.
alter table incidents add column kind text not null default 'check';

--bun:split

alter table incidents add column slo_uid text references slos(uid) on delete set null;

--bun:split

alter table incidents add column slo_alert_policy_uid text references slo_alert_policies(uid) on delete set null;

--bun:split

-- Dedup enforced by the database: at most ONE open burn incident per
-- (SLO, policy). See the Postgres file for the racing-replica rationale.
create unique index if not exists uq_active_slo_burn_incident
  on incidents (slo_uid, slo_alert_policy_uid)
  where state = 1 and kind = 'slo_burn' and deleted_at is null;

--bun:split

create index if not exists idx_incidents_kind_check_uid
  on incidents (kind, check_uid)
  where deleted_at is null;

-- ==========================================================================
-- SECTION: audit-actor-metadata
-- Actor metadata on events, for the security/config audit trail
-- (spec 2026-08-21-09).
-- ==========================================================================

-- SQLite mirror of the audit-actor-metadata section of
-- postgres/migrations/015_v0_18_0.up.sql. See the Postgres file for why the
-- spec's `actor_user_uid` is the pre-existing `actor_uid` column rather than a
-- new one.
--
-- Postgres can widen the actor_type domain with two ALTERs. SQLite cannot drop
-- a CHECK constraint, and `actor_type text not null check (actor_type in
-- ('system','user'))` has been baked into this table since 001 — so admitting
-- 'api_token' / 'service' means the established *_new rebuild pattern (same
-- technique as the status-page-password section above). source_ip / user_agent
-- are added as part of the rebuild rather than by separate ALTERs, so this
-- section is one atomic shape change instead of three.
--
-- events is not itself FK-referenced by anything, but it DOES reference
-- organizations / incidents / checks / users, so foreign_keys is still cycled
-- off around the swap for the same reason as every other rebuild here: with it
-- ON, the drop-and-rename dance is evaluated against live parent rows.
--
-- The PRAGMA statements are isolated with --bun:split so they execute on the
-- migration connection in autocommit — a PRAGMA foreign_keys issued inside a
-- transaction is silently a no-op.
--
-- The INSERT column list is spelled out explicitly (never `select *`), because
-- `insert ... select` is positional and a silent column-order drift here would
-- scramble the whole audit trail.

--bun:split

PRAGMA foreign_keys=OFF;

--bun:split

create table events_new (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade, -- Owning organization
  incident_uid      text references incidents(uid) on delete cascade, -- Related incident. NULL for non-incident events
  check_uid         text references checks(uid) on delete cascade, -- Related check. NULL for non-check events
  job_uid           text, -- Related background job (e.g., notification delivery)
  event_type        text not null, -- Event type: check.created, auth.login_failed, member.role_changed, etc.
  -- Who triggered the event. 'api_token' = a personal access token or agent
  -- key; 'service' = a signed service-to-service call.
  actor_type        text not null check (actor_type in ('system', 'user', 'api_token', 'service')),
  actor_uid         text references users(uid), -- Acting user (the spec's "actor_user_uid"). NULL for system events
  payload           text, -- Event-specific data as JSON
  created_at        text not null default (datetime('now')),
  source_ip         text, -- Client address. NULL when unknown or audit.capture_ip is off
  user_agent        text  -- Raw User-Agent header, truncated. NULL when absent
);

--bun:split

insert into events_new (
  uid, organization_uid, incident_uid, check_uid, job_uid,
  event_type, actor_type, actor_uid, payload, created_at
)
select
  uid, organization_uid, incident_uid, check_uid, job_uid,
  event_type, actor_type, actor_uid, payload, created_at
from events;

--bun:split

drop table events;

--bun:split

alter table events_new rename to events;

--bun:split

-- Every index the dropped table carried, recreated verbatim.
create index idx_events_org_created on events (organization_uid, created_at desc);

--bun:split

create index idx_events_org_incident_created on events (organization_uid, incident_uid, created_at) where incident_uid is not null;

--bun:split

create index idx_events_check_created on events (check_uid, created_at desc) where check_uid is not null;

--bun:split

create index idx_events_type_created on events (event_type, created_at desc);

--bun:split

create index idx_events_actor on events (actor_uid, created_at desc) where actor_uid is not null;

--bun:split

-- New in this section: the audit UI's org+family+time query, and the retention
-- sweep's created_at-only scan.
create index if not exists idx_events_org_type_created
  on events (organization_uid, event_type, created_at desc);

--bun:split

create index if not exists idx_events_created on events (created_at);

--bun:split

PRAGMA foreign_keys=ON;
