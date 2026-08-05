-- Teardown/parity only — never run in production. Reverses 009_v0_8_0.up.sql.
-- Blocks are reversed in the opposite order they were applied in the up
-- migration: the slug-rebuild block (applied second) is undone first, while
-- status_pages still has its `settings` column, then the `settings` column
-- itself is dropped.

-- reverse entity slug max length raised to 100 chars (spec 2026-08-04-01).
-- Same *_new rebuild pattern as the up migration, restoring the original
-- 40/50-char length CHECK constraints. See the up migration for why
-- foreign_keys must be disabled (and isolated with --bun:split) for the
-- duration of this block, and why INSERT column lists are spelled out
-- explicitly instead of `select *`.

PRAGMA foreign_keys=OFF;

--bun:split

create table check_groups_new (
  uid                   text primary key,
  organization_uid      text not null references organizations(uid) on delete cascade,
  name                  text not null,
  slug                  text not null check (length(slug) >= 3 and length(slug) <= 40),
  description           text,
  sort_order            integer not null default 0,
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
  organization_uid               text not null references organizations(uid) on delete cascade,
  check_group_uid                text references check_groups(uid) on delete set null,
  name                            text,
  slug                            text check (slug is null or (length(slug) >= 3 and length(slug) <= 50)),
  description                     text,
  type                            text not null,
  config                          text,
  regions                         text,
  enabled                         integer not null default 1,
  internal                        integer not null default 0,
  period                          text not null default '00:01:00',
  escalation_threshold            integer not null default 10,
  reopen_cooldown_multiplier      integer,
  status                          integer not null default 0,
  status_streak                   integer not null default 0,
  status_changed_at               text,
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
  organization_uid           text not null references organizations(uid) on delete cascade,
  name                       text not null,
  slug                       text not null check (slug is null or (length(slug) >= 3 and length(slug) <= 40)),
  description                text,
  visibility                 text not null default 'public' check (visibility in ('public', 'private')),
  is_default                 integer not null default 0,
  enabled                    integer not null default 1,
  show_availability          integer not null default 1,
  show_response_time         integer not null default 1,
  history_days               integer not null default 90,
  language                   text,
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
  status_page_uid   text not null references status_pages(uid) on delete cascade,
  name              text not null,
  slug              text not null check (slug is null or (length(slug) >= 3 and length(slug) <= 40)),
  position          integer not null default 0,
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

--bun:split

-- reverse per-status-page availability color thresholds (spec 2026-08-03-01)
alter table status_pages drop column settings;
