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
