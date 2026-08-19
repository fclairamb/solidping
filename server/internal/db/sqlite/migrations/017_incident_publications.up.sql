-- SQLite mirror of postgres/migrations/017_incident_publications.up.sql — the
-- incident publication overlay (spec 2026-08-19-08). See the Postgres file for
-- the full rationale, in particular why nothing derived from an incident's
-- probe diagnostics has a column to land in here.
--
-- Numbering: scratch migration for the still-unreleased v0.17.0 cycle; folds
-- into 014_v0_17_0 at release consolidation.
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
