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
