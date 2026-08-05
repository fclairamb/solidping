-- solidping v0.8.0 — see the postgres twin for the full rationale.
--
-- Multiple features share this one release migration; each contributes a
-- clearly separated block below. Append new blocks at the end.

-- ---------------------------------------------------------------------------
-- Per-status-page availability color thresholds (spec 2026-08-03-01)
-- ---------------------------------------------------------------------------
-- Generic per-page customization column, typed on the Go model
-- (models.StatusPageSettings). SQLite has no jsonb type, so it is stored as
-- text (the cross-database convention used across these migrations).

alter table status_pages add column settings text not null default '{}'; -- Per-page display customization (availability thresholds today). Typed on the Go model.

-- ---------------------------------------------------------------------------
-- Entity slug max length raised to 100 chars (spec 2026-08-04-01)
-- ---------------------------------------------------------------------------
-- check_groups/checks/status_pages/status_page_sections had regex-equivalent
-- length CHECK constraints (40/50 chars) that drifted from, and now block,
-- the widened application-level slugRegex (3-100 chars) in
-- server/internal/handlers/{checks,checkgroups,statuspages}/service.go.
-- SQLite has no ALTER COLUMN or DROP/ADD CONSTRAINT, so each table is rebuilt
-- with the established *_new pattern (same technique and same FK rationale as
-- 005_v0_4_0.up.sql's escalation_policies/on_call_schedules rebuild). These
-- four tables are FK-referenced by other live tables (checks, results,
-- incidents, status_page_resources, maintenance_window_checks, ...); with
-- foreign_keys=ON a DROP TABLE on a referenced parent fires its children's ON
-- DELETE actions against the still-live child rows (e.g. nulling every
-- checks.check_group_uid) *before* the rebuilt table is swapped back in. The
-- PRAGMA statements are isolated with --bun:split so they execute on the
-- migration connection in autocommit (a PRAGMA foreign_keys issued inside a
-- transaction is silently a no-op) — see 005_v0_4_0.up.sql. INSERT column
-- lists are spelled out explicitly (not `select *`) since `insert ... select`
-- is positional: a silent column-count/order drift here would scramble data
-- across every existing row. Severities has no length CHECK constraint at
-- all, so it needs no rebuild. Org slugs, worker slugs, and other resources
-- are deliberately left untouched.

PRAGMA foreign_keys=OFF;

--bun:split

create table check_groups_new (
  uid                   text primary key,
  organization_uid      text not null references organizations(uid) on delete cascade, -- Owning organization
  name                  text not null, -- Display name for the group
  slug                  text not null check (length(slug) >= 3 and length(slug) <= 100), -- URL-friendly identifier, unique per organization
  description           text, -- Optional description of what this group contains
  sort_order            integer not null default 0, -- Display order (lower = higher). Default 0
  escalation_policy_uid text references escalation_policies(uid) on delete set null,
  created_at            text not null default (datetime('now')),
  updated_at            text not null default (datetime('now')),
  deleted_at            text
);

insert into check_groups_new (
  uid, organization_uid, name, slug, description, sort_order, escalation_policy_uid,
  created_at, updated_at, deleted_at
)
select
  uid, organization_uid, name, slug, description, sort_order, escalation_policy_uid,
  created_at, updated_at, deleted_at
from check_groups;

drop table check_groups;
alter table check_groups_new rename to check_groups;

create unique index check_groups_org_slug_idx on check_groups (organization_uid, slug) where deleted_at is null;
create index check_groups_org_idx on check_groups (organization_uid) where deleted_at is null;

--bun:split

create table checks_new (
  uid                            text primary key,
  organization_uid               text not null references organizations(uid) on delete cascade, -- Owning organization
  check_group_uid                text references check_groups(uid) on delete set null, -- Optional group. NULL means ungrouped
  name                            text, -- Human-readable check name
  slug                           text check (slug is null or (length(slug) >= 3 and length(slug) <= 100)), -- URL-friendly identifier, unique per org. NULL allowed
  description                    text, -- Documentation describing what this check monitors and why
  type                            text not null, -- Check protocol: http, tcp, ping, dns, ssl, etc.
  config                          text, -- Protocol-specific configuration (URL, port, timeout, expected status, etc.)
  regions                         text, -- Regions where this check runs. NULL or empty means all regions
  enabled                         integer not null default 1, -- Whether the check is actively scheduled for execution
  internal                        integer not null default 0, -- Internal checks are hidden from public status pages
  period                          text not null default '00:01:00', -- Execution frequency (e.g., 00:01:00 = 1 minute)
  escalation_threshold            integer not null default 10, -- Consecutive failures before escalating an incident
  reopen_cooldown_multiplier      integer, -- Multiplier for adaptive cooldown before reopening. NULL uses system default
  status                          integer not null default 0, -- Current status: 0=unknown, 1=up, 2=down, 3=timeout, 4=error
  status_streak                   integer not null default 0, -- Consecutive results with the same status
  status_changed_at               text, -- When the status last changed
  escalation_policy_uid           text references escalation_policies(uid) on delete set null,
  config_private                  text,
  config_private_keys             text,
  confirmation_period_seconds     integer not null default 0,
  recovery_period_seconds         integer not null default 0,
  first_failure_at                text,
  first_success_since_failure_at  text,
  created_at                      text not null default (datetime('now')),
  updated_at                      text not null default (datetime('now')),
  deleted_at                      text,
  flapping_window_seconds         integer not null default 21600,
  flap_backoff_factor             integer not null default 2,
  max_recovery_multiplier         integer not null default 8,
  flap_count                      integer not null default 0,
  last_outage_at                  text,
  config_sealed                   text,
  region_spread                   text
);

insert into checks_new (
  uid, organization_uid, check_group_uid, name, slug, description, type, config, regions,
  enabled, internal, period, escalation_threshold, reopen_cooldown_multiplier, status,
  status_streak, status_changed_at, escalation_policy_uid, config_private, config_private_keys,
  confirmation_period_seconds, recovery_period_seconds, first_failure_at,
  first_success_since_failure_at, created_at, updated_at, deleted_at, flapping_window_seconds,
  flap_backoff_factor, max_recovery_multiplier, flap_count, last_outage_at, config_sealed,
  region_spread
)
select
  uid, organization_uid, check_group_uid, name, slug, description, type, config, regions,
  enabled, internal, period, escalation_threshold, reopen_cooldown_multiplier, status,
  status_streak, status_changed_at, escalation_policy_uid, config_private, config_private_keys,
  confirmation_period_seconds, recovery_period_seconds, first_failure_at,
  first_success_since_failure_at, created_at, updated_at, deleted_at, flapping_window_seconds,
  flap_backoff_factor, max_recovery_multiplier, flap_count, last_outage_at, config_sealed,
  region_spread
from checks;

drop table checks;
alter table checks_new rename to checks;

create unique index checks_slug_idx on checks (organization_uid, slug) where deleted_at is null and slug is not null;
create index checks_group_idx on checks (check_group_uid) where check_group_uid is not null and deleted_at is null;
create index idx_checks_email_token on checks (json_extract(config, '$.token'))
  where type = 'email' and deleted_at is null;

--bun:split

create table status_pages_new (
  uid                        text primary key,
  organization_uid           text not null references organizations(uid) on delete cascade, -- Owning organization
  name                       text not null, -- Page title displayed to visitors
  slug                       text not null check (slug is null or (length(slug) >= 3 and length(slug) <= 100)), -- URL-friendly identifier, unique per organization
  description                text, -- Subtitle or description shown on the page
  visibility                 text not null default 'public' check (visibility in ('public', 'private')), -- Access control: public or private
  is_default                 integer not null default 0, -- At most one default page per org
  enabled                    integer not null default 1, -- Whether the page is accessible
  show_availability          integer not null default 1, -- Whether to display uptime percentage
  show_response_time         integer not null default 1, -- Whether to display response time charts
  history_days               integer not null default 90, -- Number of days of history to display
  language                   text, -- ISO language code (e.g., en, fr). NULL uses system default
  created_at                 text not null default (datetime('now')),
  updated_at                 text not null default (datetime('now')),
  deleted_at                 text,
  history_period              text not null default '90d',
  custom_domain                text,
  custom_domain_token          text,
  custom_domain_verified_at    text,
  custom_domain_checked_at     text,
  custom_domain_failures       integer not null default 0,
  custom_css                   text,
  settings                     text not null default '{}'
);

insert into status_pages_new (
  uid, organization_uid, name, slug, description, visibility, is_default, enabled,
  show_availability, show_response_time, history_days, language, created_at, updated_at,
  deleted_at, history_period, custom_domain, custom_domain_token, custom_domain_verified_at,
  custom_domain_checked_at, custom_domain_failures, custom_css, settings
)
select
  uid, organization_uid, name, slug, description, visibility, is_default, enabled,
  show_availability, show_response_time, history_days, language, created_at, updated_at,
  deleted_at, history_period, custom_domain, custom_domain_token, custom_domain_verified_at,
  custom_domain_checked_at, custom_domain_failures, custom_css, settings
from status_pages;

drop table status_pages;
alter table status_pages_new rename to status_pages;

create unique index status_pages_org_slug_idx on status_pages (organization_uid, slug) where deleted_at is null;
create unique index status_pages_org_default_idx on status_pages (organization_uid) where is_default = 1 and deleted_at is null;
create unique index status_pages_custom_domain_idx
  on status_pages (custom_domain)
  where custom_domain is not null and deleted_at is null;

--bun:split

create table status_page_sections_new (
  uid               text primary key,
  status_page_uid   text not null references status_pages(uid) on delete cascade, -- Parent status page
  name              text not null, -- Section heading displayed on the page
  slug              text not null check (slug is null or (length(slug) >= 3 and length(slug) <= 100)), -- URL-friendly identifier, unique per status page
  position          integer not null default 0, -- Display order (lower = higher on page)
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text
);

insert into status_page_sections_new (
  uid, status_page_uid, name, slug, position, created_at, updated_at, deleted_at
)
select
  uid, status_page_uid, name, slug, position, created_at, updated_at, deleted_at
from status_page_sections;

drop table status_page_sections;
alter table status_page_sections_new rename to status_page_sections;

create unique index status_page_sections_page_slug_idx on status_page_sections (status_page_uid, slug) where deleted_at is null;
create index status_page_sections_page_idx on status_page_sections (status_page_uid) where deleted_at is null;

--bun:split

PRAGMA foreign_keys=ON;
