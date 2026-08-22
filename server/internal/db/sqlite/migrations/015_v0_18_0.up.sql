-- v0.18.0 — SQLite mirror of the ONE consolidated migration for the (still
-- unreleased) v0.18.0 release. 014_v0_17_0 is the last RELEASED migration (tag
-- v0.17.0), so everything this cycle produces lands here, in a single file per
-- dialect, per the repo convention documented in wiki/conventions/database.md.
--
-- It is organised into SECTIONs mirroring postgres/migrations/015_v0_18_0.up.sql
-- one for one, each one preserving that section's own rationale:
--
--   SECTION: generic-attachments        files.topic/details attachment link
--   SECTION: status-page-branding       status_pages logo/favicon/hide_branding
--   SECTION: status-page-password       status_pages password_hash
--   SECTION: status-subscriber-channels status_page_subscriber webhook/slack
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
-- Status-page brand assets and the white-label opt-in (spec 2026-08-21-07).
-- ==========================================================================

-- SQLite mirror of the status-page-branding section of
-- postgres/migrations/015_v0_18_0.up.sql. See the Postgres file for why the
-- asset columns hold a files.uid rather than a URL, and why hide_branding is
-- only half of the white-label decision.
--
-- Dialect difference: SQLite has no `ADD COLUMN IF NOT EXISTS`, so these
-- statements are not re-runnable — same as every other section in this set.
alter table status_pages add column logo_file_uid text;

--bun:split

alter table status_pages add column favicon_file_uid text;

--bun:split

alter table status_pages add column hide_branding boolean not null default 0;

--bun:split

create index if not exists status_pages_logo_file_idx
  on status_pages (logo_file_uid)
  where deleted_at is null and logo_file_uid is not null;

--bun:split

create index if not exists status_pages_favicon_file_idx
  on status_pages (favicon_file_uid)
  where deleted_at is null and favicon_file_uid is not null;

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
  logo_file_uid              text,
  favicon_file_uid           text,
  hide_branding              boolean not null default 0,
  password_hash              text
);

--bun:split

insert into status_pages_new (
  uid, organization_uid, name, slug, description, visibility, is_default, enabled,
  show_availability, show_response_time, history_days, language, created_at, updated_at,
  deleted_at, history_period, custom_domain, custom_domain_token, custom_domain_verified_at,
  custom_domain_checked_at, custom_domain_failures, custom_css, settings,
  auto_publish, auto_publish_delay_seconds, auto_resolve,
  logo_file_uid, favicon_file_uid, hide_branding
)
select
  uid, organization_uid, name, slug, description, visibility, is_default, enabled,
  show_availability, show_response_time, history_days, language, created_at, updated_at,
  deleted_at, history_period, custom_domain, custom_domain_token, custom_domain_verified_at,
  custom_domain_checked_at, custom_domain_failures, custom_css, settings,
  auto_publish, auto_publish_delay_seconds, auto_resolve,
  logo_file_uid, favicon_file_uid, hide_branding
from status_pages;

--bun:split

drop table status_pages;

--bun:split

alter table status_pages_new rename to status_pages;

--bun:split

-- Every index the dropped table carried, recreated verbatim — including the
-- two the status-page-branding section above had just added, which the DROP
-- took with it.
create unique index status_pages_org_slug_idx on status_pages (organization_uid, slug) where deleted_at is null;

--bun:split

create unique index status_pages_org_default_idx on status_pages (organization_uid) where is_default = 1 and deleted_at is null;

--bun:split

create unique index status_pages_custom_domain_idx
  on status_pages (custom_domain)
  where custom_domain is not null and deleted_at is null;

--bun:split

create index status_pages_logo_file_idx
  on status_pages (logo_file_uid)
  where deleted_at is null and logo_file_uid is not null;

--bun:split

create index status_pages_favicon_file_idx
  on status_pages (favicon_file_uid)
  where deleted_at is null and favicon_file_uid is not null;

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
