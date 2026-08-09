-- Teardown/parity only — never run in production. Reverses 012_org_profile.up.sql.

drop index if exists organization_previous_slugs_organization_idx;

--bun:split

drop index if exists organization_previous_slugs_slug_idx;

--bun:split

drop table if exists organization_previous_slugs;

--bun:split

alter table organizations drop column logo_file_uid;

--bun:split

alter table organizations drop column logo_url;


-- --- from 011_owner_role ---------------------------------------------------

-- Teardown/parity only — never run in production. Reverses 011_owner_role.up.sql.

update organization_members set role = 'admin' where role = 'owner';

PRAGMA foreign_keys=OFF;

--bun:split

create table organization_members_new (
  uid               text primary key,
  user_uid          text not null references users(uid) on delete cascade,
  organization_uid  text not null references organizations(uid) on delete cascade,
  role              text not null check (role in ('admin', 'user', 'viewer')),
  invited_by_uid    text references users(uid) on delete set null,
  invited_at        text,
  joined_at         text,
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text
);

insert into organization_members_new (
  uid, user_uid, organization_uid, role, invited_by_uid, invited_at, joined_at,
  created_at, updated_at, deleted_at
)
select
  uid, user_uid, organization_uid, role, invited_by_uid, invited_at, joined_at,
  created_at, updated_at, deleted_at
from organization_members;

drop table organization_members;
alter table organization_members_new rename to organization_members;

create unique index organization_members_user_org_idx on organization_members (user_uid, organization_uid) where deleted_at is null;
create index organization_members_org_idx on organization_members (organization_uid) where deleted_at is null;
create index organization_members_user_idx on organization_members (user_uid) where deleted_at is null;

--bun:split

PRAGMA foreign_keys=ON;


-- --- original 010 ----------------------------------------------------------

-- Teardown/parity only — never run in production. Reverses 010_v0_10_0.up.sql.

-- reverse OAuth 2.0 device authorization grant (spec 2026-08-08-02)
drop table if exists device_auth_requests;
