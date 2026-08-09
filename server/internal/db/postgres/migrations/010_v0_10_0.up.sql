-- solidping v0.10.0 — consolidated release migration.
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
-- the minted PAT here and the first successful poll deletes the row, so the
-- token is delivered exactly once. A background purge sweeps rows whose human
-- never showed up.

create table device_auth_requests (
  uid               uuid primary key default gen_random_uuid(),
  device_code       text not null,
  user_code         text not null,
  client_name       text not null,
  status            text not null default 'pending',
  organization_uid  uuid references organizations(uid) on delete cascade,
  user_uid          uuid references users(uid) on delete cascade,
  token_uid         uuid,
  token_value       text,
  last_polled_at    timestamptz,
  expires_at        timestamptz not null,
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now(),
  constraint device_auth_requests_status_check
    check (status in ('pending', 'approved', 'denied'))
);

create unique index device_auth_requests_device_code_idx on device_auth_requests (device_code);
create unique index device_auth_requests_user_code_idx on device_auth_requests (user_code);
create index device_auth_requests_expires_at_idx on device_auth_requests (expires_at);

comment on table device_auth_requests is
  'Pending RFC 8628 device-authorization requests (sp auth login). Short-lived and single-use: the first successful poll after approval deletes the row.';
comment on column device_auth_requests.device_code is
  'The requesting client''s secret (32 random bytes, hex). This is the real capability — never shown in a browser.';
comment on column device_auth_requests.user_code is
  'Canonical (uppercase, dashless) short code the human types on the consent page. Brute-forceable surface — the consent lookup is rate limited.';
comment on column device_auth_requests.organization_uid is
  'Organization selected by the approving user at consent time. The minted PAT is scoped to it — NOT implicitly to the approver''s session org.';
comment on column device_auth_requests.token_value is
  'The minted PAT, stashed between approval and the client''s next poll, then deleted with the row. Same at-rest exposure as user_tokens.token.';


-- ---------------------------------------------------------------------------
-- Folded in from scratch migration 011_owner_role (spec 2026-08-08-11).
-- ---------------------------------------------------------------------------

-- Organization owner role (spec 2026-08-08-11).
--
-- Adds `owner` above `admin` in the member-role hierarchy
-- (owner > admin > user > viewer). The org creator becomes its owner, and only
-- an owner may delete the organization or grant/revoke ownership.
--
-- Scratch migration for the in-flight v0.10.0 cycle; it is folded into the
-- consolidated release migration at release time (see wiki/conventions/database.md).

alter table organization_members drop constraint if exists organization_members_role_check;
alter table organization_members
  add constraint organization_members_role_check
  check (role in ('owner', 'admin', 'user', 'viewer'));

comment on column organization_members.role is
  'Role: owner (full access + org deletion + ownership grants), admin (full access), user (read/write), viewer (read-only).';

-- Backfill: every pre-existing org ends up with exactly one owner — its oldest
-- live admin. Orgs that somehow have no admin at all are left alone (there is
-- nobody to promote); orgs that already have an owner are skipped.
update organization_members as m
set role = 'owner',
    updated_at = now()
from (
  select distinct on (om.organization_uid) om.uid
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
  order by om.organization_uid, om.created_at asc, om.uid asc
) as oldest
where m.uid = oldest.uid;


-- ---------------------------------------------------------------------------
-- Folded in from scratch migration 012_org_profile (spec 2026-08-08-12).
-- ---------------------------------------------------------------------------

-- Editable organization profile: name, slug and logo (spec 2026-08-08-12).
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
--      redirects to the current one.
--
--      An alias NEVER shadows a live organization — resolution always tries
--      organizations.slug first — and it is released the moment another org
--      claims the slug. The partial unique index enforces that at most one
--      live alias exists per slug; the app releases (soft-deletes) it on
--      CreateOrg and on rename.
--
--      Deleted orgs are explicitly out of scope (spec 2026-08-08-11): the
--      alias lookup joins organizations and requires deleted_at is null, so a
--      soft-deleted org can never be reached through its previous slug.
--
-- Scratch migration for the in-flight v0.10.0 cycle; it is folded into the
-- consolidated release migration at release time (see wiki/conventions/database.md).

alter table organizations add column if not exists logo_url text;
alter table organizations add column if not exists logo_file_uid uuid references files(uid) on delete set null;

comment on column organizations.logo_url is
  'Organization logo: an external http(s) URL, or /pub/org-logos/<uid> for an uploaded file. NULL = default logo.';
comment on column organizations.logo_file_uid is
  'File backing logo_url when the logo was uploaded rather than linked. NULL for external URLs.';

create table if not exists organization_previous_slugs (
  uid              uuid primary key default gen_random_uuid(),
  organization_uid uuid not null references organizations(uid) on delete cascade,
  slug             text not null,
  created_at       timestamptz not null default now(),
  deleted_at       timestamptz
);

create unique index if not exists organization_previous_slugs_slug_idx
  on organization_previous_slugs (slug) where deleted_at is null;
create index if not exists organization_previous_slugs_organization_idx
  on organization_previous_slugs (organization_uid) where deleted_at is null;
