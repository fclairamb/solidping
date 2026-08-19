-- Teardown/parity only — never run in production. Reverses
-- 017_incident_publications.up.sql. Same *_new rebuild technique as the up
-- migration, restoring the original status_updates shape.

PRAGMA foreign_keys=OFF;

--bun:split

-- Rows created by the auto-publish pipeline have no author and cannot survive
-- a NOT NULL author_uid.
delete from status_updates where author_uid is null;

--bun:split

create table status_updates_old (
  uid              text primary key,
  organization_uid text not null references organizations(uid),
  status_page_uid  text not null references status_pages(uid),
  section_uid      text references status_page_sections(uid),
  check_uid        text references checks(uid),
  incident_uid     text references incidents(uid),
  title            text not null,
  body_markdown    text not null,
  link_url         text,
  kind             text not null check (kind in ('investigating', 'identified', 'monitoring', 'resolved', 'maintenance', 'info')),
  published_at     text not null default (datetime('now')),
  author_uid       text not null references users(uid),
  created_at       text not null default (datetime('now')),
  updated_at       text not null default (datetime('now')),
  deleted_at       text
);

insert into status_updates_old (
  uid, organization_uid, status_page_uid, section_uid, check_uid, incident_uid,
  title, body_markdown, link_url, kind, published_at, author_uid, created_at,
  updated_at, deleted_at
)
select
  uid, organization_uid, status_page_uid, section_uid, check_uid, incident_uid,
  title, body_markdown, link_url, kind, published_at, author_uid, created_at,
  updated_at, deleted_at
from status_updates;

drop table status_updates;
alter table status_updates_old rename to status_updates;

create index idx_status_updates_org_page_pub on status_updates (organization_uid, status_page_uid, published_at desc);
create index idx_status_updates_incident on status_updates (incident_uid);
create index idx_status_updates_check on status_updates (check_uid);

--bun:split

PRAGMA foreign_keys=ON;

--bun:split

-- status_pages / status_page_resources: SQLite can DROP COLUMN for plain
-- columns that carry no unique constraint, which is the case for all four.
alter table status_page_resources drop column auto_publish;

--bun:split

alter table status_pages drop column auto_resolve;

--bun:split

alter table status_pages drop column auto_publish_delay_seconds;

--bun:split

alter table status_pages drop column auto_publish;

--bun:split

drop table if exists incident_publications;
