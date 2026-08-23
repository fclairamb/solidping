-- Teardown/parity half of the consolidated v0.18.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of 015_v0_18_0.up.sql,
-- so each one unwinds on a schema that still has everything the sections above
-- it created.
--
-- Several sections are lossy on the way down; each says so in its own note.

-- ==========================================================================
-- SECTION: traceroute-diagnostics
-- Teardown half of the traceroute-diagnostics section (spec 2026-08-21-10).
-- ==========================================================================

-- LOSSY: per-check overrides are dropped with the column, returning every
-- check to inheriting the org default.
alter table checks drop column traceroute_on_failure;

--bun:split

-- ==========================================================================
-- SECTION: audit-actor-metadata
-- Teardown half of the audit-actor-metadata section (spec 2026-08-21-09).
-- ==========================================================================

-- LOSSY the same way the Postgres half is: events attributed to an 'api_token'
-- or 'service' actor are deleted rather than relabelled, because relabelling
-- them 'system' would rewrite history inside an audit trail.
--
-- SQLite still cannot alter a CHECK constraint, so narrowing actor_type back to
-- ('system','user') and dropping source_ip / user_agent is one more *_new
-- rebuild — the exact inverse of the up half.

PRAGMA foreign_keys=OFF;

--bun:split

delete from events where actor_type in ('api_token', 'service');

--bun:split

create table events_old (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade,
  incident_uid      text references incidents(uid) on delete cascade,
  check_uid         text references checks(uid) on delete cascade,
  job_uid           text,
  event_type        text not null,
  actor_type        text not null check (actor_type in ('system', 'user')),
  actor_uid         text references users(uid),
  payload           text,
  created_at        text not null default (datetime('now'))
);

--bun:split

insert into events_old (
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

alter table events_old rename to events;

--bun:split

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

PRAGMA foreign_keys=ON;

--bun:split

-- ==========================================================================
-- SECTION: slo-burn-alerts
-- Teardown half of the slo-burn-alerts section (spec 2026-08-21-08).
-- ==========================================================================

-- LOSSY the same way the Postgres half is: burn incidents are deleted rather
-- than downgraded, because a surviving row would read as an ordinary check
-- incident once `kind` is gone.

delete from incidents where kind = 'slo_burn';

--bun:split

drop index if exists idx_incidents_kind_check_uid;

--bun:split

drop index if exists uq_active_slo_burn_incident;

--bun:split

-- Modern SQLite (3.35+) supports DROP COLUMN, and none of these three is
-- referenced by an index or a generated column at this point, so no rebuild is
-- needed on the way down either.
alter table incidents drop column slo_alert_policy_uid;

--bun:split

alter table incidents drop column slo_uid;

--bun:split

alter table incidents drop column kind;

--bun:split

drop table if exists slo_alert_policies;

--bun:split

-- ==========================================================================
-- SECTION: status-subscriber-channels
-- Teardown half of the status-subscriber-channels section (spec 2026-08-21-07).
-- ==========================================================================

-- LOSSY the same way the Postgres half is: every webhook and Slack
-- subscription is deleted rather than downgraded, because nothing in the
-- restored schema can hold a delivery endpoint.
--
-- SQLite needs the rebuild again to restore `email text not null`.

PRAGMA foreign_keys=OFF;

--bun:split

delete from status_page_subscriber where channel <> 'email';

--bun:split

create table status_page_subscriber_old (
  uid               text primary key,
  organization_uid  text not null references organizations(uid),
  status_page_uid   text not null references status_pages(uid),
  email             text not null,
  confirmed_at      text,
  confirm_token     text not null,
  unsubscribe_token text not null,
  scope             text not null check (scope in ('page', 'incident')),
  incident_uid      text references incidents(uid),
  created_at        text not null default (datetime('now')),
  deleted_at        text
);

--bun:split

insert into status_page_subscriber_old (
  uid, organization_uid, status_page_uid, email, confirmed_at, confirm_token,
  unsubscribe_token, scope, incident_uid, created_at, deleted_at
)
select
  uid, organization_uid, status_page_uid, coalesce(email, ''), confirmed_at, confirm_token,
  unsubscribe_token, scope, incident_uid, created_at, deleted_at
from status_page_subscriber;

--bun:split

drop table status_page_subscriber;

--bun:split

alter table status_page_subscriber_old rename to status_page_subscriber;

--bun:split

create unique index idx_status_page_subscriber_confirm_token on status_page_subscriber (confirm_token);

--bun:split

create unique index idx_status_page_subscriber_unsub_token on status_page_subscriber (unsubscribe_token);

--bun:split

create index idx_status_page_subscriber_page_confirmed on status_page_subscriber (status_page_uid, confirmed_at);

--bun:split

create unique index idx_status_page_subscriber_live
  on status_page_subscriber (status_page_uid, email, scope, coalesce(incident_uid, ''))
  where deleted_at is null;

--bun:split

PRAGMA foreign_keys=ON;

-- ==========================================================================
-- SECTION: status-page-password
-- Teardown half of the status-page-password section (spec 2026-08-21-07).
-- ==========================================================================

-- LOSSY, and more so than the Postgres half.
--
-- Dropping the hash makes every password page unopenable rather than public —
-- the API refuses to serve a `password` page it cannot check a password for.
-- That is the safe direction, but it takes those pages offline until an
-- operator re-sets a password or moves them back to `public`.
--
-- The widened CHECK constraint is NOT narrowed back: doing so would need a
-- second full table rebuild, and it would FAIL outright on any database still
-- holding a `password` row. Instead the rows are moved back to `private` first
-- (the safe reading: a page that was shared behind a secret must not become
-- world-readable because someone rolled a migration back), and the constraint
-- is left permissive. A permissive constraint on a database whose application
-- code predates the value is inert — models.ValidStatusPageVisibility is the
-- real gate.

update status_pages set visibility = 'private' where visibility = 'password';

--bun:split

alter table status_pages drop column password_hash;

-- ==========================================================================
-- SECTION: status-page-branding
-- Teardown half of the status-page-branding section
-- (specs 2026-08-21-07, 2026-08-22-03).
-- ==========================================================================

-- SQLite mirror of the status-page-branding teardown in
-- postgres/migrations/015_v0_18_0.down.sql. No columns to drop — branding
-- lives in `status_pages.settings`, which this migration did not create.

update organizations
   set logo_url = '/pub/org-logos/' || logo_file_uid
 where logo_file_uid is not null
   and logo_url like '/pub/assets/%';

--bun:split

update files
   set topic = null
 where topic like 'organizations/%/logo';

-- ==========================================================================
-- SECTION: generic-attachments
-- Was scratch migration 020_generic_attachments (spec 2026-08-21-01). Teardown half.
-- ==========================================================================

-- Teardown/parity only — never run in production. SQLite mirror of the
-- generic-attachments section of postgres/migrations/015_v0_18_0.down.sql.
--
-- LOSSY the same way the Postgres half is: dropping the columns destroys every
-- attachment link while leaving the blobs and their `files` rows behind.

drop index if exists files_org_topic_idx;

--bun:split

alter table files drop column details;

--bun:split

alter table files drop column topic;
