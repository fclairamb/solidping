-- solidping v0.10.0 — consolidated release migration.
-- SQLite mirror of the Postgres migration.
--
-- Multiple features share this one release migration; each contributes a
-- clearly separated block below. Append new blocks at the end.

-- ---------------------------------------------------------------------------
-- OAuth 2.0 Device Authorization Grant, RFC 8628 (spec 2026-08-08-02)
-- ---------------------------------------------------------------------------
-- Pending device-authorization requests for `sp auth login`. The CLI opens a
-- request, shows the short user_code, and polls the token endpoint with the
-- device_code until a logged-in human approves it in the dashboard.
--
-- Rows are short-lived (15 min) and single-use: the approving request stashes
-- the minted PAT here (token_value) and the first successful poll deletes the
-- row, so the token is delivered exactly once. device_code is the real
-- capability; user_code is the brute-forceable surface and the consent lookup
-- is rate limited.

create table device_auth_requests (
  uid               text primary key,
  device_code       text not null,
  user_code         text not null,
  client_name       text not null,
  status            text not null default 'pending',
  organization_uid  text references organizations(uid) on delete cascade, -- Org selected at consent time; the minted PAT is scoped to it
  user_uid          text references users(uid) on delete cascade, -- Approving user
  token_uid         text,
  token_value       text, -- Minted PAT, deleted with the row on first delivery
  last_polled_at    text,
  expires_at        text not null,
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  constraint device_auth_requests_status_check
    check (status in ('pending', 'approved', 'denied'))
);

create unique index device_auth_requests_device_code_idx on device_auth_requests (device_code);
create unique index device_auth_requests_user_code_idx on device_auth_requests (user_code);
create index device_auth_requests_expires_at_idx on device_auth_requests (expires_at);


-- ---------------------------------------------------------------------------
-- Folded in from scratch migration 011_owner_role (spec 2026-08-08-11).
-- ---------------------------------------------------------------------------

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


-- ---------------------------------------------------------------------------
-- Folded in from scratch migration 012_org_profile (spec 2026-08-08-12).
-- ---------------------------------------------------------------------------

-- Editable organization profile: name, slug and logo (spec 2026-08-08-12).
-- SQLite mirror of the Postgres migration.
--
-- Two independent additions:
--
--   1. organizations.logo_url / organizations.logo_file_uid — the org's logo,
--      either an external http(s) URL or an uploaded file served from
--      /pub/org-logos/<file uid>. logo_file_uid is what lets a replacement
--      upload retire the previous blob and what authorizes the unsigned
--      public logo route (the file must be the CURRENT logo of a live org).
--
--   2. organization_previous_slugs — the rename alias store. Renaming an org
--      no longer breaks the URLs customers pasted elsewhere (status pages,
--      SVG badges, the /embed/v1 widget): the previous slug is remembered and
--      redirects to the current one. An alias never shadows a live org
--      (resolution tries organizations.slug first) and is released the moment
--      another org claims the slug. A soft-deleted org can never be reached
--      through an alias — the lookup joins organizations and requires
--      deleted_at is null (spec 2026-08-08-11 boundary).
--
-- ALTER TABLE ... ADD COLUMN with a REFERENCES clause is legal in SQLite only
-- for a nullable column defaulting to NULL, which is exactly the case here —
-- so no *_new table rebuild is needed.

alter table organizations add column logo_url text; -- Logo: external http(s) URL, or /pub/org-logos/<uid> for an upload. NULL = default logo

--bun:split

alter table organizations add column logo_file_uid text references files(uid) on delete set null; -- File backing logo_url when uploaded. NULL for external URLs

--bun:split

create table if not exists organization_previous_slugs (
  uid              text primary key,
  organization_uid text not null references organizations(uid) on delete cascade, -- Organization that used to answer on this slug
  slug             text not null, -- The freed slug; redirects to the org's current slug until someone else claims it
  created_at       text not null default (datetime('now')),
  deleted_at       text -- Set when the alias is released (another org claimed the slug, or the org renamed back)
);

--bun:split

create unique index if not exists organization_previous_slugs_slug_idx
  on organization_previous_slugs (slug) where deleted_at is null;

--bun:split

create index if not exists organization_previous_slugs_organization_idx
  on organization_previous_slugs (organization_uid) where deleted_at is null;
