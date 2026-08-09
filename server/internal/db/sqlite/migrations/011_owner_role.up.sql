-- Organization owner role (spec 2026-08-08-11).
-- SQLite mirror of the Postgres migration.
--
-- Adds `owner` above `admin` in the member-role hierarchy
-- (owner > admin > user > viewer). The org creator becomes its owner, and only
-- an owner may delete the organization or grant/revoke ownership.
--
-- SQLite has no DROP/ADD CONSTRAINT, so organization_members is rebuilt with
-- the established *_new pattern (same technique and same FK rationale as
-- 005_v0_4_0.up.sql / 009_v0_8_0.up.sql). The PRAGMA statements are isolated
-- with --bun:split so they execute in autocommit — a PRAGMA foreign_keys
-- issued inside a transaction is silently a no-op. INSERT column lists are
-- spelled out explicitly (never `select *`): `insert ... select` is positional
-- and a silent column drift would scramble every membership row.

PRAGMA foreign_keys=OFF;

--bun:split

create table organization_members_new (
  uid               text primary key,
  user_uid          text not null references users(uid) on delete cascade, -- Member user
  organization_uid  text not null references organizations(uid) on delete cascade, -- Organization the user belongs to
  role              text not null check (role in ('owner', 'admin', 'user', 'viewer')), -- Role: owner (full access + org deletion + ownership grants), admin (full access), user (read/write), viewer (read-only)
  invited_by_uid    text references users(uid) on delete set null, -- User who sent the invitation. NULL for founders or migrated users
  invited_at        text, -- When the invitation was sent. NULL for immediate additions
  joined_at         text, -- When the user accepted the invitation. NULL means pending
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

-- Backfill: every pre-existing org ends up with exactly one owner — its oldest
-- live admin. Orgs that have no admin at all are left alone (there is nobody to
-- promote); orgs that already have an owner are skipped.
update organization_members
set role = 'owner',
    updated_at = datetime('now')
where uid in (
  select (
    select inner_m.uid
    from organization_members inner_m
    where inner_m.organization_uid = om.organization_uid
      and inner_m.role = 'admin'
      and inner_m.deleted_at is null
    order by inner_m.created_at asc, inner_m.uid asc
    limit 1
  )
  from organization_members om
  where om.role = 'admin'
    and om.deleted_at is null
    and not exists (
      select 1
      from organization_members owner_m
      where owner_m.organization_uid = om.organization_uid
        and owner_m.role = 'owner'
        and owner_m.deleted_at is null
    )
);

--bun:split

PRAGMA foreign_keys=ON;
